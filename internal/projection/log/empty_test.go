package log

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestChannelIsEmptyDetectsAnyRow(t *testing.T) {
	inserts := []struct {
		name      string
		statement string
	}{
		{name: "replay_log", statement: `INSERT INTO replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, is_document, msg_date) VALUES (?, 1, 'put', '{}', 'h', '` + HashHeader("h") + `', 0, 0)`},
		{name: "replay_log_rejects", statement: `INSERT INTO replay_log_rejects (channel_id, msg_id, code, raw_header, msg_date) VALUES (?, 1, 'malformed', 'h', 0)`},
		{name: "buckets", statement: `INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id) VALUES (?, 'photos', 'private', 'none', 0, 1, '` + testJobID + `', 0, 1)`},
		{name: "object_tombstones", statement: `INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, writer, msg_date) VALUES (?, 'k', 5, '` + testJobID + `', 'w', 0)`},
		{name: "object_bodies", statement: `INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (?, 4, '` + testJobID + `', 10, 0, 0)`},
	}
	for _, tt := range inserts {
		t.Run(tt.name, func(t *testing.T) {
			db := openProjectionDB(t)
			check := func() bool {
				var empty bool
				err := db.Read(context.Background(), func(tx *sql.Tx) error {
					var err error
					empty, err = ChannelIsEmpty(context.Background(), tx, testChannel)
					return err
				})
				if err != nil {
					t.Fatalf("empty check: %v", err)
				}
				return empty
			}
			if !check() {
				t.Fatal("a freshly migrated channel should be empty")
			}
			err := db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(tt.statement, testChannel)
				return err
			})
			if err != nil {
				t.Fatalf("insert %s: %v", tt.name, err)
			}
			if check() {
				t.Fatalf("channel should not be empty after a %s row", tt.name)
			}
		})
	}
}

func TestChannelIsEmptyAfterProjectFromOp(t *testing.T) {
	db := openProjectionDB(t)
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpBucketCreate: insertBucketApplier()})

	if _, err := projector.ProjectFromOp(context.Background(), db, observed(1, formatted(t, bucketCreateOp(testJobID, 3600)))); err != nil {
		t.Fatalf("project: %v", err)
	}
	var empty bool
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		empty, err = ChannelIsEmpty(context.Background(), tx, testChannel)
		return err
	})
	if err != nil || empty {
		t.Fatalf("empty = %v, error = %v; want a non-empty channel", empty, err)
	}
	if !errors.Is(ErrChannelNotEmpty, ErrChannelNotEmpty) {
		t.Fatal("sentinel must match itself")
	}
}
