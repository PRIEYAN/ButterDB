package objectrepo

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const testChannel int64 = 1001

const testJobID = "00000000-0000-4000-8000-000000000000"

func openDB(t *testing.T) *sqlitedb.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func newRepo(t *testing.T) (*Repository, *sqlitedb.DB) {
	t.Helper()
	db := openDB(t)
	repo, err := New(db)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repo, db
}

func sampleMeta(channelID int64, key string, putMsgID int64) object.ObjectMeta {
	return object.ObjectMeta{
		ChannelID:     channelID,
		Key:           key,
		PutMsgID:      putMsgID,
		BodyMsgID:     putMsgID - 1,
		JobID:         testJobID,
		StoredSize:    10,
		PlaintextSize: 10,
		ETag:          strings.Repeat("a", 32),
		SHA256:        strings.Repeat("b", 64),
		ContentType:   "application/octet-stream",
		Writer:        "w",
		MsgDate:       1700000000,
	}
}

func putAll(t *testing.T, db *sqlitedb.DB, metas ...object.ObjectMeta) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, meta := range metas {
			if err := PutObject(context.Background(), tx, meta, 1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed objects: %v", err)
	}
}

func putKeys(t *testing.T, db *sqlitedb.DB, keys ...string) {
	t.Helper()
	metas := make([]object.ObjectMeta, len(keys))
	for i, key := range keys {
		metas[i] = sampleMeta(testChannel, key, int64(i+2))
	}
	putAll(t, db, metas...)
}

func execAll(t *testing.T, db *sqlitedb.DB, statements ...string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(context.Background(), statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func keysOf(page object.ListPage) []string {
	keys := make([]string, len(page.Objects))
	for i, meta := range page.Objects {
		keys[i] = meta.Key
	}
	return keys
}

func explain(t *testing.T, db *sqlitedb.DB, statement string, args ...any) []string {
	t.Helper()
	rows, err := db.Reader().Query("EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return details
}
