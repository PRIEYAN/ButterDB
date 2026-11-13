package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

//go:embed projection/*.sql control/*.sql
var files embed.FS

type Set string

const (
	Projection Set = "projection"
	Control    Set = "control"
)

type Migration struct {
	Version int
	Name    string
	SHA256  string
	SQL     string
}

var (
	ErrSchemaTooNew   = errors.New("schema: database schema is newer than this binary")
	ErrSchemaChecksum = errors.New("schema: applied migration differs from the embedded file")
	ErrSchemaGap      = errors.New("schema: embedded migrations are not contiguous")
	ErrWrongDatabase  = sqlitedb.ErrWrongDatabase
)

const createSchemaVersion = `CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER PRIMARY KEY CHECK (version >= 1),
  name       TEXT    NOT NULL,
  sha256     TEXT    NOT NULL CHECK (length(sha256) = 64),
  applied_at INTEGER NOT NULL
) STRICT;`

const migrationSuffix = ".sql"

func Migrations(set Set) ([]Migration, error) {
	dir, err := directoryOf(set)
	if err != nil {
		return nil, err
	}
	return loadMigrations(files, dir)
}

func Latest(set Set) int {
	migrations, err := Migrations(set)
	if err != nil || len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].Version
}

func directoryOf(set Set) (string, error) {
	switch set {
	case Projection, Control:
		return string(set), nil
	}
	return "", fmt.Errorf("schema: unknown migration set %q", string(set))
}

func loadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("schema: read %s migrations: %w", dir, err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		version, ok := parseMigrationName(entry.Name())
		if !ok || entry.IsDir() {
			return nil, fmt.Errorf("%w: %s/%s does not match NNNN_name.sql", ErrSchemaGap, dir, entry.Name())
		}
		body, err := fs.ReadFile(fsys, dir+"/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("schema: read %s/%s: %w", dir, entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{
			Version: version,
			Name:    strings.TrimSuffix(entry.Name(), migrationSuffix),
			SHA256:  hex.EncodeToString(sum[:]),
			SQL:     string(body),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for i, migration := range migrations {
		if migration.Version != i+1 {
			return nil, fmt.Errorf("%w: %s has no version %d before version %d", ErrSchemaGap, dir, i+1, migration.Version)
		}
	}
	return migrations, nil
}

func parseMigrationName(name string) (int, bool) {
	stem, found := strings.CutSuffix(name, migrationSuffix)
	if !found || len(stem) < 6 || stem[4] != '_' {
		return 0, false
	}
	for _, digit := range stem[:4] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	for _, r := range stem[5:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return 0, false
		}
	}
	version, err := strconv.Atoi(stem[:4])
	if err != nil {
		return 0, false
	}
	return version, true
}

type appliedRow struct {
	version int
	name    string
	sha256  string
}

func Current(ctx context.Context, db *sqlitedb.DB) (int, error) {
	rows, err := readApplied(ctx, db)
	if err != nil {
		return 0, err
	}
	current := 0
	for _, row := range rows {
		current = max(current, row.version)
	}
	return current, nil
}

func Pending(ctx context.Context, db *sqlitedb.DB, set Set) ([]Migration, error) {
	migrations, err := Migrations(set)
	if err != nil {
		return nil, err
	}
	if err := checkIdentity(ctx, db, set, false); err != nil {
		return nil, err
	}
	rows, err := readApplied(ctx, db)
	if err != nil {
		return nil, err
	}
	applied, err := verifyApplied(rows, migrations)
	if err != nil {
		return nil, err
	}
	return pendingOf(migrations, applied), nil
}

func Apply(ctx context.Context, db *sqlitedb.DB, set Set) ([]int, error) {
	migrations, err := Migrations(set)
	if err != nil {
		return nil, err
	}
	return applyMigrations(ctx, db, set, migrations)
}

func applyMigrations(ctx context.Context, db *sqlitedb.DB, set Set, migrations []Migration) ([]int, error) {
	if err := checkIdentity(ctx, db, set, true); err != nil {
		return nil, err
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, createSchemaVersion)
		return err
	}); err != nil {
		return nil, fmt.Errorf("schema: create schema_version: %w", err)
	}
	rows, err := readApplied(ctx, db)
	if err != nil {
		return nil, err
	}
	applied, err := verifyApplied(rows, migrations)
	if err != nil {
		return nil, err
	}
	done := []int{}
	for _, migration := range pendingOf(migrations, applied) {
		if err := applyOne(ctx, db, migration); err != nil {
			return done, err
		}
		done = append(done, migration.Version)
	}
	if err := integrityChecks(ctx, db, set); err != nil {
		return done, err
	}
	return done, nil
}

func applyOne(ctx context.Context, db *sqlitedb.DB, migration Migration) error {
	appliedAt := db.Now().UnixMilli()
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO schema_version (version, name, sha256, applied_at) VALUES (?, ?, ?, ?)",
			migration.Version, migration.Name, migration.SHA256, appliedAt)
		return err
	})
	if err != nil {
		return fmt.Errorf("schema: apply migration %04d_%s: %w", migration.Version, migration.Name, err)
	}
	return nil
}

func readApplied(ctx context.Context, db *sqlitedb.DB) ([]appliedRow, error) {
	var rows []appliedRow
	err := db.Read(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'").Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return nil
		}
		result, err := tx.QueryContext(ctx, "SELECT version, name, sha256 FROM schema_version ORDER BY version")
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var row appliedRow
			if err := result.Scan(&row.version, &row.name, &row.sha256); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return result.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("schema: read schema_version: %w", err)
	}
	return rows, nil
}

func verifyApplied(rows []appliedRow, migrations []Migration) (map[int]bool, error) {
	applied := make(map[int]bool, len(rows))
	for _, row := range rows {
		if row.version < 1 || row.version > len(migrations) {
			return nil, fmt.Errorf("%w: database is at version %d, this binary knows %d", ErrSchemaTooNew, row.version, len(migrations))
		}
		if migrations[row.version-1].SHA256 != row.sha256 {
			return nil, fmt.Errorf("%w: version %d", ErrSchemaChecksum, row.version)
		}
		applied[row.version] = true
	}
	return applied, nil
}

func pendingOf(migrations []Migration, applied map[int]bool) []Migration {
	pending := []Migration{}
	for _, migration := range migrations {
		if !applied[migration.Version] {
			pending = append(pending, migration)
		}
	}
	return pending
}

func checkIdentity(ctx context.Context, db *sqlitedb.DB, set Set, stamp bool) error {
	if _, err := directoryOf(set); err != nil {
		return err
	}
	if db.Name() != sqlitedb.Name(set) {
		return fmt.Errorf("schema: %w: handle is %s, set is %s", ErrWrongDatabase, db.Name(), set)
	}
	want, err := sqlitedb.ApplicationID(db.Name())
	if err != nil {
		return err
	}
	var id int
	if err := db.Reader().QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return fmt.Errorf("schema: read application id: %w", err)
	}
	if id == want {
		return nil
	}
	if id != 0 {
		return fmt.Errorf("schema: %w: %s", ErrWrongDatabase, set)
	}
	if !stamp {
		return nil
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", want))
		return err
	}); err != nil {
		return fmt.Errorf("schema: stamp application id: %w", err)
	}
	return nil
}

func integrityChecks(ctx context.Context, db *sqlitedb.DB, set Set) error {
	err := db.Read(ctx, func(tx *sql.Tx) error {
		if set == Control {
			if err := expectNoRows(ctx, tx, "PRAGMA foreign_key_check"); err != nil {
				return fmt.Errorf("foreign_key_check: %w", err)
			}
		}
		return expectOK(ctx, tx, "PRAGMA quick_check")
	})
	if err != nil {
		return fmt.Errorf("schema: integrity check: %w", err)
	}
	return nil
}

func expectNoRows(ctx context.Context, tx *sql.Tx, statement string) error {
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("reported a problem")
	}
	return rows.Err()
}

func expectOK(ctx context.Context, tx *sql.Tx, statement string) error {
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			return errors.New("quick_check reported a problem")
		}
	}
	return rows.Err()
}
