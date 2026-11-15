package keyrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var ErrDuplicateKey = errors.New("keyrepo: access key id already exists")

type Database interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Record struct {
	ID           access.AccessKeyID
	Name         string
	SecretSealed []byte
	Scope        access.Scope
	CreatedAt    int64
	CreatedBy    string
	UpdatedAt    int64
	LastUsedAt   int64
	RevokedAt    int64
}

type Repository struct {
	db Database
}

func New(db Database) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("keyrepo: %w: database", errs.ErrMissingDependency)
	}
	return &Repository{db: db}, nil
}

const selectColumns = `access_key_id, name, secret_sealed, scope_buckets, scope_actions,
  created_at, created_by, updated_at, last_used_at, revoked_at`

const (
	insertKey = `INSERT INTO access_keys (access_key_id, name, secret_sealed, scope_buckets, scope_actions,
  created_at, created_by, updated_at, last_used_at, revoked_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)`
	selectByID    = `SELECT ` + selectColumns + ` FROM access_keys WHERE access_key_id = ?1`
	selectActive  = `SELECT ` + selectColumns + ` FROM access_keys WHERE revoked_at IS NULL ORDER BY created_at DESC, access_key_id`
	selectAll     = `SELECT ` + selectColumns + ` FROM access_keys ORDER BY created_at DESC, access_key_id`
	updateActive  = `UPDATE access_keys SET name = ?2, scope_buckets = ?3, scope_actions = ?4, updated_at = ?5 WHERE access_key_id = ?1 AND revoked_at IS NULL`
	revokeKey     = `UPDATE access_keys SET revoked_at = ?2, updated_at = ?2 WHERE access_key_id = ?1 AND revoked_at IS NULL`
	touchLastUsed = `UPDATE access_keys SET last_used_at = ?2 WHERE access_key_id = ?1`
)

type rowScanner interface {
	Scan(dest ...any) error
}

func (r *Repository) Insert(ctx context.Context, rec Record) error {
	buckets, actions, err := encodeScope(rec.Scope)
	if err != nil {
		return fmt.Errorf("keyrepo: insert: %w", err)
	}
	err = r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertKey, string(rec.ID), rec.Name, rec.SecretSealed, buckets, actions,
			rec.CreatedAt, rec.CreatedBy, rec.UpdatedAt, rec.LastUsedAt, nullableMillis(rec.RevokedAt))
		return err
	})
	if isUniqueViolation(err) {
		return fmt.Errorf("keyrepo: insert: %w", ErrDuplicateKey)
	}
	if err != nil {
		return fmt.Errorf("keyrepo: insert: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id access.AccessKeyID) (Record, error) {
	var rec Record
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		rec, err = scanOne(ctx, tx, id)
		return err
	})
	if err != nil {
		return Record{}, fmt.Errorf("keyrepo: get: %w", err)
	}
	return rec, nil
}

func (r *Repository) List(ctx context.Context, includeRevoked bool) ([]Record, error) {
	statement := selectActive
	if includeRevoked {
		statement = selectAll
	}
	records := []Record{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, statement)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanRecord(rows)
			if err != nil {
				return err
			}
			records = append(records, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("keyrepo: list: %w", err)
	}
	return records, nil
}

func (r *Repository) Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope, now time.Time) (Record, error) {
	var rec Record
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		current, err := scanOne(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.RevokedAt != 0 {
			return errs.ErrAccessKeyRevoked
		}
		if name != nil {
			current.Name = *name
		}
		if scope != nil {
			current.Scope = *scope
		}
		current.UpdatedAt = now.UnixMilli()
		buckets, actions, err := encodeScope(current.Scope)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, updateActive, string(id), current.Name, buckets, actions, current.UpdatedAt); err != nil {
			return err
		}
		rec = current
		return nil
	})
	if err != nil {
		return Record{}, fmt.Errorf("keyrepo: update: %w", err)
	}
	return rec, nil
}

func (r *Repository) Revoke(ctx context.Context, id access.AccessKeyID, now time.Time) (Record, error) {
	var rec Record
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := scanOne(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, revokeKey, string(id), now.UnixMilli()); err != nil {
			return err
		}
		var err error
		rec, err = scanOne(ctx, tx, id)
		return err
	})
	if err != nil {
		return Record{}, fmt.Errorf("keyrepo: revoke: %w", err)
	}
	return rec, nil
}

func (r *Repository) TouchLastUsed(ctx context.Context, id access.AccessKeyID, now time.Time) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, touchLastUsed, string(id), now.UnixMilli())
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return errs.ErrAccessKeyNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("keyrepo: touch last used: %w", err)
	}
	return nil
}

func scanOne(ctx context.Context, tx *sql.Tx, id access.AccessKeyID) (Record, error) {
	rec, err := scanRecord(tx.QueryRowContext(ctx, selectByID, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errs.ErrAccessKeyNotFound
	}
	return rec, err
}

func scanRecord(row rowScanner) (Record, error) {
	var (
		rec       Record
		id        string
		buckets   string
		actions   string
		revokedAt sql.NullInt64
	)
	err := row.Scan(&id, &rec.Name, &rec.SecretSealed, &buckets, &actions,
		&rec.CreatedAt, &rec.CreatedBy, &rec.UpdatedAt, &rec.LastUsedAt, &revokedAt)
	if err != nil {
		return Record{}, err
	}
	rec.ID = access.AccessKeyID(id)
	rec.RevokedAt = revokedAt.Int64
	rec.Scope, err = decodeScope(buckets, actions)
	if err != nil {
		return Record{}, err
	}
	return rec, nil
}

func encodeScope(scope access.Scope) (string, string, error) {
	buckets := scope.Buckets()
	actions := scope.Actions()
	if len(buckets) == 0 || len(actions) == 0 {
		return "", "", fmt.Errorf("%w: scope is empty", access.ErrInvalidScope)
	}
	encodedBuckets, err := json.Marshal(buckets)
	if err != nil {
		return "", "", err
	}
	encodedActions, err := json.Marshal(actions)
	if err != nil {
		return "", "", err
	}
	return string(encodedBuckets), string(encodedActions), nil
}

func decodeScope(buckets, actions string) (access.Scope, error) {
	var bucketNames, actionNames []string
	if err := json.Unmarshal([]byte(buckets), &bucketNames); err != nil {
		return access.Scope{}, fmt.Errorf("decode scope buckets: %w", err)
	}
	if err := json.Unmarshal([]byte(actions), &actionNames); err != nil {
		return access.Scope{}, fmt.Errorf("decode scope actions: %w", err)
	}
	return access.NewScope(bucketNames, actionNames)
}

func nullableMillis(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY || code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}
