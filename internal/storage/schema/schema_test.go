package schema

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const specPath = "../../../docs/spec/schema.md"

var projectionTables = []string{
	"channels", "encryption_keys", "buckets", "deleted_messages", "initial_scan_spool",
	"object_bodies", "object_tombstones", "objects", "pending_channel_cleanup",
	"pending_message_cleanup", "projection_operations", "registry_markers",
	"replay_log", "replay_log_rejects", "replay_log_tamper",
}

var controlTables = []string{
	"access_keys", "admin_audit", "admin_devices", "admin_meta", "admin_pairing_codes",
	"admin_setup_token", "delete_jobs", "idempotency_keys", "multipart_parts",
	"multipart_uploads", "replica_channels", "replica_jobs", "replica_objects",
	"snapshots", "telegram_accounts", "upload_jobs",
}

func openDB(t *testing.T, name sqlitedb.Name, path string) *sqlitedb.DB {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), string(name)+".db")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), name, path, sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableNames(t *testing.T, db *sqlitedb.DB) []string {
	t.Helper()
	rows, err := db.Reader().Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMigrationsContiguous(t *testing.T) {
	name := regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)
	for _, set := range []Set{Projection, Control} {
		t.Run(string(set), func(t *testing.T) {
			migrations, err := Migrations(set)
			if err != nil {
				t.Fatalf("migrations: %v", err)
			}
			if len(migrations) == 0 {
				t.Fatal("no migrations")
			}
			for i, migration := range migrations {
				if migration.Version != i+1 {
					t.Errorf("migration %d has version %d", i, migration.Version)
				}
				if !name.MatchString(migration.Name + ".sql") {
					t.Errorf("name %q does not match the pattern", migration.Name)
				}
				if len(migration.SHA256) != 64 || migration.SQL == "" {
					t.Errorf("migration %d has an empty body or short hash", migration.Version)
				}
			}
			if got := Latest(set); got != len(migrations) {
				t.Errorf("latest = %d, want %d", got, len(migrations))
			}
		})
	}
	if _, err := Migrations(Set("other")); err == nil {
		t.Error("unknown set accepted")
	}
}

func TestLoadMigrationsRejects(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"gap after one", map[string]string{"s/0001_a.sql": "x", "s/0003_b.sql": "x"}},
		{"starts at two", map[string]string{"s/0002_a.sql": "x"}},
		{"duplicate number", map[string]string{"s/0001_a.sql": "x", "s/0001_b.sql": "x"}},
		{"upper case name", map[string]string{"s/0001_A.sql": "x"}},
		{"short number", map[string]string{"s/001_a.sql": "x"}},
		{"missing name", map[string]string{"s/0001_.sql": "x"}},
		{"wrong suffix", map[string]string{"s/0001_a.txt": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, body := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			if _, err := loadMigrations(fsys, "s"); !errors.Is(err, ErrSchemaGap) {
				t.Fatalf("err = %v, want ErrSchemaGap", err)
			}
		})
	}
}

func TestApplyFreshAndIdempotent(t *testing.T) {
	cases := []struct {
		set    Set
		name   sqlitedb.Name
		tables []string
	}{
		{Projection, sqlitedb.Projection, projectionTables},
		{Control, sqlitedb.Control, controlTables},
	}
	for _, tc := range cases {
		t.Run(string(tc.set), func(t *testing.T) {
			ctx := context.Background()
			db := openDB(t, tc.name, "")
			applied, err := Apply(ctx, db, tc.set)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if len(applied) != Latest(tc.set) || applied[0] != 1 {
				t.Errorf("applied = %v, want 1..%d", applied, Latest(tc.set))
			}
			want := sorted(append([]string{"schema_version"}, tc.tables...))
			if got := tableNames(t, db); !equalStrings(got, want) {
				t.Errorf("tables = %v, want %v", got, want)
			}
			current, err := Current(ctx, db)
			if err != nil || current != Latest(tc.set) {
				t.Errorf("current = %d, err = %v, want %d", current, err, Latest(tc.set))
			}
			again, err := Apply(ctx, db, tc.set)
			if err != nil || len(again) != 0 {
				t.Errorf("second apply = %v, err = %v, want nothing applied", again, err)
			}
			pending, err := Pending(ctx, db, tc.set)
			if err != nil || len(pending) != 0 {
				t.Errorf("pending = %v, err = %v, want none", pending, err)
			}
			var id int
			if err := db.Reader().QueryRow("PRAGMA application_id").Scan(&id); err != nil {
				t.Fatal(err)
			}
			wantID, _ := sqlitedb.ApplicationID(tc.name)
			if id != wantID {
				t.Errorf("application_id = %d, want %d", id, wantID)
			}
		})
	}
}

func TestSpecTablesAreCreated(t *testing.T) {
	spec := readSpec(t)
	create := regexp.MustCompile(`(?m)^CREATE TABLE ([a-z_]+) \(`)
	cases := []struct {
		name  string
		block string
		want  []string
	}{
		{"projection", specBlock(t, spec, "### 4.1 "), projectionTables},
		{"control", specBlock(t, spec, "### 5.1 "), controlTables},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, match := range create.FindAllStringSubmatch(tc.block, -1) {
				got = append(got, match[1])
			}
			if !equalStrings(sorted(got), sorted(tc.want)) {
				t.Errorf("spec tables = %v, want %v", sorted(got), sorted(tc.want))
			}
		})
	}
}

func TestApplyRecordsRowAndClock(t *testing.T) {
	ctx := context.Background()
	fake := clock.NewFake(time.UnixMilli(1700000123456))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, filepath.Join(t.TempDir(), "control.db"), sqlitedb.Options{}, logger,
		sqlitedb.Dependencies{Clock: fake, FS: fsx.OS{}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Apply(ctx, db, Control); err != nil {
		t.Fatalf("apply: %v", err)
	}
	migrations, _ := Migrations(Control)
	var version int
	var name, sum string
	var appliedAt int64
	err = db.Reader().QueryRow("SELECT version, name, sha256, applied_at FROM schema_version").Scan(&version, &name, &sum, &appliedAt)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || name != migrations[0].Name || sum != migrations[0].SHA256 || appliedAt != 1700000123456 {
		t.Errorf("row = %d %s %s %d", version, name, sum, appliedAt)
	}
}

func TestApplyStopsAtFailingMigration(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, sqlitedb.Projection, "")
	migrations := []Migration{
		{Version: 1, Name: "0001_one", SHA256: strings.Repeat("a", 64), SQL: "CREATE TABLE one (id INTEGER PRIMARY KEY) STRICT;"},
		{Version: 2, Name: "0002_two", SHA256: strings.Repeat("b", 64), SQL: "CREATE TABLE two (id INTEGER PRIMARY KEY) STRICT; CREATE TABLE one (id INTEGER);"},
	}
	applied, err := applyMigrations(ctx, db, Projection, migrations)
	if err == nil {
		t.Fatal("apply succeeded")
	}
	if len(applied) != 1 || applied[0] != 1 {
		t.Errorf("applied = %v, want [1]", applied)
	}
	got := tableNames(t, db)
	if !equalStrings(got, []string{"one", "schema_version"}) {
		t.Errorf("tables = %v, want one and schema_version only", got)
	}
}

func TestSchemaTooNew(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, sqlitedb.Projection, "")
	if _, err := Apply(ctx, db, Projection); err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := Latest(Projection) + 1
	err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO schema_version (version, name, sha256, applied_at) VALUES (?, 'future', ?, 0)", next, strings.Repeat("c", 64))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, db, Projection); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("apply: err = %v, want ErrSchemaTooNew", err)
	}
	if _, err := Pending(ctx, db, Projection); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("pending: err = %v, want ErrSchemaTooNew", err)
	}
}

func TestSchemaChecksum(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, sqlitedb.Control, "")
	if _, err := Apply(ctx, db, Control); err != nil {
		t.Fatalf("apply: %v", err)
	}
	err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE schema_version SET sha256 = ? WHERE version = 1", strings.Repeat("0", 64))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, db, Control); !errors.Is(err, ErrSchemaChecksum) {
		t.Errorf("apply: err = %v, want ErrSchemaChecksum", err)
	}
	if _, err := Pending(ctx, db, Control); !errors.Is(err, ErrSchemaChecksum) {
		t.Errorf("pending: err = %v, want ErrSchemaChecksum", err)
	}
}

func TestWrongDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "control.db")
	control := openDB(t, sqlitedb.Control, path)
	if _, err := Apply(ctx, control, Control); err != nil {
		t.Fatalf("apply control: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := sqlitedb.Open(ctx, sqlitedb.Projection, path, sqlitedb.Options{}, logger); !errors.Is(err, ErrWrongDatabase) {
		t.Errorf("open control file as projection: err = %v, want ErrWrongDatabase", err)
	}

	projection := openDB(t, sqlitedb.Projection, "")
	if _, err := Apply(ctx, projection, Control); !errors.Is(err, ErrWrongDatabase) {
		t.Errorf("apply control set on a projection handle: err = %v, want ErrWrongDatabase", err)
	}
	if _, err := Pending(ctx, projection, Control); !errors.Is(err, ErrWrongDatabase) {
		t.Errorf("pending control set on a projection handle: err = %v, want ErrWrongDatabase", err)
	}
	if _, err := Apply(ctx, projection, Set("other")); err == nil {
		t.Error("unknown set accepted")
	}
}

func TestPendingOnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, sqlitedb.Projection, "")
	pending, err := Pending(ctx, db, Projection)
	if err != nil || len(pending) != Latest(Projection) {
		t.Fatalf("pending = %d, err = %v, want %d", len(pending), err, Latest(Projection))
	}
	current, err := Current(ctx, db)
	if err != nil || current != 0 {
		t.Fatalf("current = %d, err = %v, want 0", current, err)
	}
	var id int
	if err := db.Reader().QueryRow("PRAGMA application_id").Scan(&id); err != nil || id != 0 {
		t.Fatalf("pending stamped the file: id = %d, err = %v", id, err)
	}
}

func readSpec(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	return string(body)
}

func specBlock(t *testing.T, spec, heading string) string {
	t.Helper()
	start := strings.Index(spec, "\n"+heading)
	if start < 0 {
		t.Fatalf("heading %q not found", heading)
	}
	rest := spec[start:]
	open := strings.Index(rest, "\n```sql\n")
	if open < 0 {
		t.Fatalf("no sql block after %q", heading)
	}
	rest = rest[open+len("\n```sql\n"):]
	closing := strings.Index(rest, "\n```\n")
	if closing < 0 {
		t.Fatalf("unterminated sql block after %q", heading)
	}
	return rest[:closing+1]
}

func TestDDLMatchesSpec(t *testing.T) {
	spec := readSpec(t)
	cases := []struct {
		set     Set
		heading string
	}{
		{Projection, "### 4.1 "},
		{Control, "### 5.1 "},
	}
	for _, tc := range cases {
		t.Run(string(tc.set), func(t *testing.T) {
			want := specBlock(t, spec, tc.heading)
			migrations, err := Migrations(tc.set)
			if err != nil {
				t.Fatal(err)
			}
			if migrations[0].Name != "0001_init" {
				t.Fatalf("first migration is %q, want 0001_init", migrations[0].Name)
			}
			if migrations[0].SQL != want {
				t.Fatalf("embedded 0001_init.sql differs from the spec block (%d bytes vs %d)", len(migrations[0].SQL), len(want))
			}
		})
	}
}
