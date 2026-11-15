package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const (
	projectionDatabaseFile = "projection.db"
	primaryRole            = "primary"
)

func (a *App) openProjectionDatabase(ctx context.Context) (*sqlitedb.DB, error) {
	dir := a.DataDir()
	if dir == nil {
		return nil, ErrAppNotStarted
	}
	db, err := sqlitedb.OpenWith(ctx, sqlitedb.Projection, dir.Join(projectionDatabaseFile), sqlitedb.Options{}, a.logger,
		sqlitedb.Dependencies{Clock: a.clock, FS: a.fsys})
	if err != nil {
		return nil, fmt.Errorf("core: open projection database: %w", err)
	}
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		_ = db.Close()
		if errors.Is(err, schema.ErrSchemaTooNew) {
			return nil, fmt.Errorf("%w: %w", ErrIncompatibleData, err)
		}
		return nil, fmt.Errorf("core: migrate projection database: %w", err)
	}
	return db, nil
}
