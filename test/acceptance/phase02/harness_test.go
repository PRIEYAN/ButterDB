package phase02

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

func openMigrated(t *testing.T, name sqlitedb.Name, set schema.Set) *sqlitedb.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), string(name)+".db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), name, path, sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, set); err != nil {
		t.Fatalf("apply %s schema: %v", set, err)
	}
	return db
}
