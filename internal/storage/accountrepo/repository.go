package accountrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var (
	ErrInvalidRecord = errors.New("accountrepo: invalid account record")
	ErrNotFound      = errors.New("accountrepo: account not found")
)

const (
	RolePrimary = "primary"
	RoleReplica = "replica"
)

type Database interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Record struct {
	Role        string
	UserID      int64
	Username    string
	PhoneMasked string
	DCID        int
	Linked      bool
	BoundAt     int64
	LinkedAt    int64
	UnlinkedAt  int64
}

type Repository struct {
	db Database
}

func New(db Database) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("accountrepo: %w: database", errs.ErrMissingDependency)
	}
	return &Repository{db: db}, nil
}

const selectColumns = `SELECT role, user_id, username, phone_masked, dc_id, linked, bound_at, linked_at, unlinked_at FROM telegram_accounts`

const upsertLinked = `INSERT INTO telegram_accounts (role, user_id, username, phone_masked, dc_id, linked, bound_at, linked_at, unlinked_at)
VALUES (?1, ?2, ?3, ?4, ?5, 1, ?6, ?7, 0)
ON CONFLICT (role) DO UPDATE SET
  user_id = excluded.user_id, username = excluded.username, phone_masked = excluded.phone_masked,
  dc_id = excluded.dc_id, linked = 1, linked_at = excluded.linked_at, unlinked_at = 0`

type rowScanner interface {
	Scan(dest ...any) error
}

func validRole(role string) bool {
	return role == RolePrimary || role == RoleReplica
}

func scanRecord(row rowScanner) (Record, error) {
	var rec Record
	var linked int
	err := row.Scan(&rec.Role, &rec.UserID, &rec.Username, &rec.PhoneMasked, &rec.DCID, &linked,
		&rec.BoundAt, &rec.LinkedAt, &rec.UnlinkedAt)
	rec.Linked = linked == 1
	return rec, err
}

func (r *Repository) Get(ctx context.Context, role string) (Record, bool, error) {
	if !validRole(role) {
		return Record{}, false, fmt.Errorf("accountrepo: get %q: %w", role, ErrInvalidRecord)
	}
	var rec Record
	found := true
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		var scanErr error
		rec, scanErr = scanRecord(tx.QueryRowContext(ctx, selectColumns+" WHERE role = ?1", role))
		if errors.Is(scanErr, sql.ErrNoRows) {
			found = false
			return nil
		}
		return scanErr
	})
	if err != nil {
		return Record{}, false, fmt.Errorf("accountrepo: get %q: %w", role, err)
	}
	return rec, found, nil
}

func (r *Repository) FindByUser(ctx context.Context, userID int64) (string, bool, error) {
	var role string
	found := true
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		scanErr := tx.QueryRowContext(ctx, "SELECT role FROM telegram_accounts WHERE user_id = ?1", userID).Scan(&role)
		if errors.Is(scanErr, sql.ErrNoRows) {
			found = false
			return nil
		}
		return scanErr
	})
	if err != nil {
		return "", false, fmt.Errorf("accountrepo: find user: %w", err)
	}
	return role, found, nil
}

func (r *Repository) List(ctx context.Context) ([]Record, error) {
	records := []Record{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectColumns+" ORDER BY role")
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
		return nil, fmt.Errorf("accountrepo: list: %w", err)
	}
	return records, nil
}

func (r *Repository) Link(ctx context.Context, rec Record) error {
	if !validRole(rec.Role) || rec.UserID <= 0 || rec.DCID < 0 {
		return fmt.Errorf("accountrepo: link %q: %w", rec.Role, ErrInvalidRecord)
	}
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, upsertLinked, rec.Role, rec.UserID, rec.Username, rec.PhoneMasked,
			rec.DCID, rec.BoundAt, rec.LinkedAt)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("accountrepo: link %q: %w", rec.Role, err)
	}
	return nil
}

func (r *Repository) Unlink(ctx context.Context, role string, at int64) error {
	if !validRole(role) {
		return fmt.Errorf("accountrepo: unlink %q: %w", role, ErrInvalidRecord)
	}
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, "UPDATE telegram_accounts SET linked = 0, unlinked_at = ?2 WHERE role = ?1", role, at)
		if execErr != nil {
			return execErr
		}
		changed, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if changed == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("accountrepo: unlink %q: %w", role, err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, role string) error {
	if !validRole(role) {
		return fmt.Errorf("accountrepo: delete %q: %w", role, ErrInvalidRecord)
	}
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, "DELETE FROM telegram_accounts WHERE role = ?1", role)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("accountrepo: delete %q: %w", role, err)
	}
	return nil
}
