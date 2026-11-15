package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

var ErrIdempotencyNotFound = errors.New("jobrepo: idempotency record not found")
var ErrDuplicateIdempotencyKey = errors.New("jobrepo: idempotency key already recorded")

type Repository struct {
	db    Database
	clock clock.Clock
}

func New(db Database, clk clock.Clock) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("jobrepo: %w: database", errs.ErrMissingDependency)
	}
	if clk == nil {
		return nil, fmt.Errorf("jobrepo: %w: clock", errs.ErrMissingDependency)
	}
	return &Repository{db: db, clock: clk}, nil
}

func (r *Repository) nowMillis() int64 {
	return r.clock.Now().UnixMilli()
}

func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY || code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func affectedOne(result sql.Result) (bool, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func rowExists(ctx context.Context, tx *sql.Tx, table, jobID string) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE job_id = ?1", jobID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func staleOrMissing(ctx context.Context, tx *sql.Tx, table, jobID string) error {
	exists, err := rowExists(ctx, tx, table, jobID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrJobNotFound
	}
	return ErrStaleTransition
}

func validScope(scope string) bool {
	for _, prefix := range []string{"key:", "device:"} {
		if strings.HasPrefix(scope, prefix) && len(scope) > len(prefix) {
			return true
		}
	}
	return false
}
