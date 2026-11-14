package log

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	testChannel    int64 = 2045123456
	testJobID            = "0b6f3d2a-7c41-4e8a-9d15-2f6c8a4b1e07"
	otherJobID           = "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f"
	testPutHash          = "77af778b51abd4a3c51c5ddd97204a9c3ae614ebccb75a606c3b6865aed6744e"
	testPutETag          = "9b2cf535f27731c974343645a3985328"
	testBucketName       = "photos"
	testMsgDate    int64 = 1763000060
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openProjectionDB(t *testing.T) *sqlitedb.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

type applyFunc func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error)

func (f applyFunc) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	return f(ctx, tx, in)
}

type fakeRegistry struct {
	appliers map[wire.OpType]projection.Applier
}

func (r fakeRegistry) Lookup(t wire.OpType) (projection.Applier, bool) {
	applier, ok := r.appliers[t]
	return applier, ok
}

func insertBucketApplier() projection.Applier {
	return applyFunc(func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO buckets
  (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Op.BucketChannelID, in.Op.Bucket, in.Op.Policy, in.Op.DefaultEncryption, in.Op.CacheMaxAge,
			in.Observed.MsgID, in.Op.JobID, in.Observed.MsgDate, in.Observed.MsgID)
		if err != nil {
			return projection.ApplyResult{}, err
		}
		return projection.ApplyResult{Kind: projection.OutcomeApplied}, nil
	})
}

func newTestProjector(t *testing.T, appliers map[wire.OpType]projection.Applier) *Projector {
	t.Helper()
	projector, err := NewProjector(Dependencies{Registry: fakeRegistry{appliers: appliers}, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	return projector
}

func bucketCreateOp(jobID string, cacheMaxAge int64) wire.Op {
	return wire.Op{
		Type:              wire.OpBucketCreate,
		JobID:             jobID,
		Bucket:            testBucketName,
		BucketChannelID:   testChannel,
		Policy:            "private",
		DefaultEncryption: "tde1",
		CacheMaxAge:       cacheMaxAge,
		Writer:            "dev.k3m9x2q7w4t1z8r6",
		Timestamp:         1763000060,
	}
}

func putOp(jobID, key string) wire.Op {
	return wire.Op{
		Type:        wire.OpPut,
		JobID:       jobID,
		Key:         key,
		BodyMsgID:   8811,
		StoredSize:  482113,
		ETag:        testPutETag,
		SHA256:      testPutHash,
		ContentType: "image/jpeg",
		Writer:      "key.BDBK7Q2M9X4T1W8Z3R6Y",
		Timestamp:   1763000200,
	}
}

func formatted(t *testing.T, op wire.Op) string {
	t.Helper()
	header, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return header
}

func observed(msgID int64, text string) projection.Observed {
	return projection.Observed{ChannelID: testChannel, MsgID: msgID, MsgDate: testMsgDate, RawText: text}
}

func countRows(t *testing.T, db *sqlitedb.DB, query string, args ...any) int {
	t.Helper()
	var count int
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&count)
	})
	if err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func rowText(t *testing.T, db *sqlitedb.DB, query string, args ...any) string {
	t.Helper()
	var text sql.NullString
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&text)
	})
	if err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	if !text.Valid {
		return "<null>"
	}
	return text.String
}

func dumpTables(t *testing.T, db *sqlitedb.DB) string {
	t.Helper()
	tables := []string{"replay_log", "replay_log_tamper", "replay_log_rejects", "projection_operations", "buckets"}
	var out string
	for _, table := range tables {
		out += dumpTable(t, db, table)
	}
	return out
}

func dumpTable(t *testing.T, db *sqlitedb.DB, table string) string {
	t.Helper()
	var out string
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT * FROM " + table + " ORDER BY 1, 2")
		if err != nil {
			return err
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				return err
			}
			out += fmt.Sprintf("%s %v\n", table, values)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	return out
}
