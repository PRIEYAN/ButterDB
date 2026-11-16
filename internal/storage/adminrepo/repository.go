package adminrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const (
	ClaimStateUnclaimed = "unclaimed"
	ClaimStateClaimed   = "claimed"

	MetaServerID         = "server_id"
	MetaBoundUserID      = "bound_user_id"
	MetaPairingFailures  = "pairing_failures"
	PrevTokenGrace       = 10 * time.Minute
	hashLength           = 32
	deliverySavedMessage = "saved_messages"
	deliveryHost         = "host"
)

var (
	ErrDeviceNotFound     = errors.New("adminrepo: device not found")
	ErrDeviceExists       = errors.New("adminrepo: device already exists")
	ErrDeviceRevoked      = errors.New("adminrepo: device revoked")
	ErrAlreadyClaimed     = errors.New("adminrepo: server already claimed")
	ErrNotClaimed         = errors.New("adminrepo: server not claimed")
	ErrSetupTokenInvalid  = errors.New("adminrepo: setup token invalid")
	ErrPairingCodeInvalid = errors.New("adminrepo: pairing code invalid")
)

type Database interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Repository struct {
	db    Database
	clock clock.Clock
}

func New(db Database, clk clock.Clock) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("adminrepo: %w: database", errs.ErrMissingDependency)
	}
	if clk == nil {
		return nil, fmt.Errorf("adminrepo: %w: clock", errs.ErrMissingDependency)
	}
	return &Repository{db: db, clock: clk}, nil
}

func (r *Repository) ClaimState(ctx context.Context) (string, error) {
	var state string
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		claimed, err := hasDevice(ctx, tx)
		if err != nil {
			return err
		}
		state = ClaimStateUnclaimed
		if claimed {
			state = ClaimStateClaimed
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("adminrepo: claim state: %w", err)
	}
	return state, nil
}

func (r *Repository) Meta(ctx context.Context, key string) (string, bool, error) {
	var value string
	found := false
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		value, found, err = readMeta(ctx, tx, key)
		return err
	})
	if err != nil {
		return "", false, fmt.Errorf("adminrepo: read meta: %w", err)
	}
	return value, found, nil
}

func (r *Repository) SetMeta(ctx context.Context, key, value string) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return writeMeta(ctx, tx, key, value)
	})
	if err != nil {
		return fmt.Errorf("adminrepo: write meta: %w", err)
	}
	return nil
}

func (r *Repository) ServerID(ctx context.Context) (string, error) {
	value, _, err := r.Meta(ctx, MetaServerID)
	return value, err
}

func readMeta(ctx context.Context, tx *sql.Tx, key string) (string, bool, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM admin_meta WHERE key = ?1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func writeMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO admin_meta (key, value) VALUES (?1, ?2) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

func hasDevice(ctx context.Context, tx *sql.Tx) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin_devices)`).Scan(&exists)
	return exists == 1, err
}

func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY || code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

func millis(t time.Time) int64 {
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func copyHash(raw []byte) ([hashLength]byte, error) {
	var out [hashLength]byte
	if len(raw) != hashLength {
		return out, fmt.Errorf("adminrepo: stored hash has %d bytes, want %d", len(raw), hashLength)
	}
	copy(out[:], raw)
	return out, nil
}
