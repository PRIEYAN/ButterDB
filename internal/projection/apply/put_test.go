package apply

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func newBucketEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.createBucket(bucketChannel, "photos", 2)
	return e
}

func (e *env) livePut(key string) int64 {
	e.t.Helper()
	return int64(e.count(`SELECT COALESCE((SELECT put_msg_id FROM objects WHERE channel_id = ?1 AND object_key = ?2), 0)`, bucketChannel, key))
}

func (e *env) cleanupReason(msgID int64) string {
	e.t.Helper()
	var reason string
	err := e.db.Reader().QueryRow(`SELECT reason FROM pending_message_cleanup WHERE channel_id = ?1 AND msg_id = ?2`, bucketChannel, msgID).Scan(&reason)
	if err != nil {
		return ""
	}
	return reason
}

func TestPutLastWriterWins(t *testing.T) {
	cases := []struct {
		name      string
		order     []int64
		wantKinds []projection.OutcomeKind
	}{
		{"in order", []int64{10, 20}, []projection.OutcomeKind{projection.OutcomeApplied, projection.OutcomeApplied}},
		{"reversed", []int64{20, 10}, []projection.OutcomeKind{projection.OutcomeApplied, projection.OutcomeSuperseded}},
		{"three shuffled", []int64{20, 30, 10}, []projection.OutcomeKind{projection.OutcomeApplied, projection.OutcomeApplied, projection.OutcomeSuperseded}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			var highest int64
			for i, msgID := range tc.order {
				op := putOp("doc.txt", msgID-1, jobID(int(msgID)))
				op.ETag = fmt.Sprintf("%032d", msgID)
				e.mustApply(bucketObs(bucketChannel, msgID), op, tc.wantKinds[i])
				if msgID > highest {
					highest = msgID
				}
			}
			if got := e.livePut("doc.txt"); got != highest {
				t.Fatalf("live put %d, want %d", got, highest)
			}
			if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 1 {
				t.Fatalf("object_count %d, want 1", n)
			}
			if n := e.count(`SELECT stored_bytes FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 100 {
				t.Fatalf("stored_bytes %d, want 100", n)
			}
		})
	}
}

func TestPutIdempotentOnSameMsgID(t *testing.T) {
	e := newBucketEnv(t)
	op := putOp("doc.txt", 9, jobID(10))
	e.mustApply(bucketObs(bucketChannel, 10), op, projection.OutcomeApplied)
	e.mustApply(bucketObs(bucketChannel, 10), op, projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM objects`); n != 1 {
		t.Fatalf("object rows %d, want 1", n)
	}
	if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 1 {
		t.Fatalf("object_count %d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM pending_message_cleanup`); n != 0 {
		t.Fatalf("cleanup rows %d, want 0", n)
	}
}

func TestSupersededQueuedForCleanup(t *testing.T) {
	t.Run("older version replaced", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 10), putOp("k", 9, jobID(10)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
		for _, msgID := range []int64{9, 10} {
			if got := e.cleanupReason(msgID); got != "superseded" {
				t.Fatalf("message %d reason %q, want superseded", msgID, got)
			}
		}
		for _, msgID := range []int64{19, 20} {
			if got := e.cleanupReason(msgID); got != "" {
				t.Fatalf("live message %d queued as %q", msgID, got)
			}
		}
	})
	t.Run("late lower put superseded on arrival", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 10), putOp("k", 9, jobID(10)), projection.OutcomeSuperseded)
		for _, msgID := range []int64{9, 10} {
			if got := e.cleanupReason(msgID); got != "superseded" {
				t.Fatalf("message %d reason %q, want superseded", msgID, got)
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM pending_message_cleanup`); n != 2 {
			t.Fatalf("cleanup rows %d, want 2", n)
		}
	})
	t.Run("live ids are never queued", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
		if err := queueIn(e, 19, 20); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if n := e.count(`SELECT COUNT(*) FROM pending_message_cleanup`); n != 0 {
			t.Fatalf("cleanup rows %d, want 0", n)
		}
	})
	t.Run("deleted ids are never queued", func(t *testing.T) {
		e := newBucketEnv(t)
		e.exec(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, 9, 'external', 0)`, bucketChannel)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 30), putOp("k", 9, jobID(30)), projection.OutcomeApplied)
		if got := e.cleanupReason(9); got != "" {
			t.Fatalf("deleted message queued as %q", got)
		}
	})
}

func queueIn(e *env, ids ...int64) error {
	return e.db.Write(context.Background(), func(tx *sql.Tx) error {
		return queueCleanup(context.Background(), tx, bucketChannel, "k", 99, cleanupSuperseded, ids...)
	})
}

func TestTombstoneBlocksOlderPut(t *testing.T) {
	t.Run("late put below tombstone is blocked", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 30), delOp("k", jobID(30)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeSuperseded)
		if got := e.livePut("k"); got != 0 {
			t.Fatalf("object resurrected at %d", got)
		}
		for _, msgID := range []int64{19, 20} {
			if got := e.cleanupReason(msgID); got != "blocked" {
				t.Fatalf("message %d reason %q, want blocked", msgID, got)
			}
		}
	})
	t.Run("delete removes older live version", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 30), delOp("k", jobID(30)), projection.OutcomeApplied)
		if got := e.livePut("k"); got != 0 {
			t.Fatalf("object survived delete at %d", got)
		}
		for _, msgID := range []int64{19, 20} {
			if got := e.cleanupReason(msgID); got != "deleted" {
				t.Fatalf("message %d reason %q, want deleted", msgID, got)
			}
		}
		if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 0 {
			t.Fatalf("object_count %d, want 0", n)
		}
		if n := e.count(`SELECT del_msg_id FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'k'`, bucketChannel); n != 30 {
			t.Fatalf("tombstone %d, want 30", n)
		}
	})
	t.Run("lower tombstone arriving late is superseded", func(t *testing.T) {
		e := newBucketEnv(t)
		e.mustApply(bucketObs(bucketChannel, 40), delOp("k", jobID(40)), projection.OutcomeApplied)
		e.mustApply(bucketObs(bucketChannel, 30), delOp("k", jobID(30)), projection.OutcomeSuperseded)
		if n := e.count(`SELECT del_msg_id FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'k'`, bucketChannel); n != 40 {
			t.Fatalf("tombstone %d, want 40", n)
		}
	})
}

func TestNewerPutAfterDeleteLives(t *testing.T) {
	e := newBucketEnv(t)
	e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
	e.mustApply(bucketObs(bucketChannel, 30), delOp("k", jobID(30)), projection.OutcomeApplied)
	e.mustApply(bucketObs(bucketChannel, 50), putOp("k", 49, jobID(50)), projection.OutcomeApplied)
	if got := e.livePut("k"); got != 50 {
		t.Fatalf("live put %d, want 50", got)
	}
	if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 1 {
		t.Fatalf("object_count %d, want 1", n)
	}
	e.mustApply(bucketObs(bucketChannel, 25), delOp("k2", jobID(25)), projection.OutcomeApplied)
	e.mustApply(bucketObs(bucketChannel, 26), delOp("k", jobID(26)), projection.OutcomeSuperseded)
	if got := e.livePut("k"); got != 50 {
		t.Fatalf("late lower tombstone removed the newer put: %d", got)
	}
}

func TestPutRejects(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		obs   projection.Observed
		op    func() wire.Op
		want  projection.RejectCode
	}{
		{
			name: "body reference not below the put",
			obs:  bucketObs(bucketChannel, 10),
			op:   func() wire.Op { return putOp("k", 10, jobID(10)) },
			want: projection.RejectBadBodyRef,
		},
		{
			name: "encrypted size mismatch",
			obs:  bucketObs(bucketChannel, 10),
			op: func() wire.Op {
				op := putOp("k", 9, jobID(10))
				op.Encrypted, op.EncryptionVersion, op.KeyID, op.PlaintextSize = true, 1, keyIDA, 10
				return op
			},
			want: projection.RejectSizeMismatch,
		},
		{
			name: "stored size above the ceiling",
			obs:  bucketObs(bucketChannel, 10),
			op: func() wire.Op {
				op := putOp("k", 9, jobID(10))
				op.StoredSize = wire.MaxStoredBytes + 1
				return op
			},
			want: projection.RejectSizeMismatch,
		},
		{
			name: "put on a document",
			obs:  bodyObs(bucketChannel, 10),
			op:   func() wire.Op { return putOp("k", 9, jobID(10)) },
			want: projection.RejectWrongCarrier,
		},
		{
			name: "put in the registry channel",
			obs:  bucketObs(registryChannel, 10),
			op:   func() wire.Op { return putOp("k", 9, jobID(10)) },
			want: projection.RejectWrongChannel,
		},
		{
			name: "put in an unknown channel",
			obs:  bucketObs(otherChannel, 10),
			op:   func() wire.Op { return putOp("k", 9, jobID(10)) },
			want: projection.RejectBucketNotFound,
		},
		{
			name: "same job at another message",
			setup: func(e *env) {
				e.exec(`INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (?1, ?2, 'put', 5, 'applied')`, bucketChannel, jobID(10))
			},
			obs:  bucketObs(bucketChannel, 10),
			op:   func() wire.Op { return putOp("k", 9, jobID(10)) },
			want: projection.RejectDuplicateOp,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			if tc.setup != nil {
				tc.setup(e)
			}
			e.mustReject(tc.obs, tc.op(), tc.want)
			if n := e.count(`SELECT COUNT(*) FROM objects`); n != 0 {
				t.Fatalf("object rows %d, want 0", n)
			}
		})
	}
}

func TestDuplicatePutQueuesOnlyPutMessage(t *testing.T) {
	e := newBucketEnv(t)
	e.exec(`INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (?1, ?2, 'put', 5, 'applied')`, bucketChannel, jobID(10))
	e.mustReject(bucketObs(bucketChannel, 10), putOp("k", 9, jobID(10)), projection.RejectDuplicateOp)
	if got := e.cleanupReason(10); got != "duplicate" {
		t.Fatalf("put reason %q, want duplicate", got)
	}
	if got := e.cleanupReason(9); got != "" {
		t.Fatalf("shared body queued as %q", got)
	}
}

func TestPutForDeletedMessageCreatesNoRow(t *testing.T) {
	e := newBucketEnv(t)
	e.mustApply(bucketObs(bucketChannel, 10), putOp("k", 9, jobID(10)), projection.OutcomeApplied)
	e.exec(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, 20, 'external', 0)`, bucketChannel)
	e.mustApply(bucketObs(bucketChannel, 20), putOp("k", 19, jobID(20)), projection.OutcomeApplied)
	if got := e.livePut("k"); got != 0 {
		t.Fatalf("live put %d, want none", got)
	}
	if got := e.cleanupReason(10); got != "superseded" {
		t.Fatalf("older put reason %q, want superseded", got)
	}
	if got := e.cleanupReason(20); got != "" {
		t.Fatalf("deleted put queued as %q", got)
	}
}

func TestPutEncryptedFieldsStored(t *testing.T) {
	e := newBucketEnv(t)
	op := putOp("secret", 9, jobID(10))
	op.Encrypted, op.EncryptionVersion, op.KeyID, op.PlaintextSize = true, 1, keyIDA, 10
	op.StoredSize = 76
	op.UserMeta = map[string]string{"owner": "me"}
	e.mustApply(bucketObs(bucketChannel, 10), op, projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE object_key = 'secret' AND encrypted = 1 AND key_id = ?1 AND plaintext_size = 10 AND user_meta_json = '{"owner":"me"}' AND msg_date = 1700000010`, keyIDA); n != 1 {
		t.Fatalf("encrypted object row not stored as expected")
	}
}

func TestCrossBucketIsolation(t *testing.T) {
	e := newBucketEnv(t)
	e.createBucket(otherChannel, "videos", 3)
	e.mustApply(bucketObs(bucketChannel, 10), putOp("same", 9, jobID(10)), projection.OutcomeApplied)
	e.mustApply(bucketObs(otherChannel, 5), putOp("same", 4, jobID(5)), projection.OutcomeApplied)
	e.mustApply(bucketObs(otherChannel, 6), delOp("same", jobID(6)), projection.OutcomeApplied)
	if got := e.livePut("same"); got != 10 {
		t.Fatalf("first bucket put %d, want 10", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, otherChannel); n != 0 {
		t.Fatalf("second bucket objects %d, want 0", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ?1`, bucketChannel); n != 0 {
		t.Fatalf("first bucket tombstones %d, want 0", n)
	}
	if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?1`, bucketChannel); n != 1 {
		t.Fatalf("first bucket count %d, want 1", n)
	}
}

func TestBodyApplier(t *testing.T) {
	e := newBucketEnv(t)
	body := wire.Op{Type: wire.OpBody, JobID: jobID(8), StoredSize: 100}
	e.mustApply(bodyObs(bucketChannel, 8), body, projection.OutcomeApplied)
	e.mustApply(bodyObs(bucketChannel, 8), body, projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM object_bodies WHERE channel_id = ?1 AND msg_id = 8`, bucketChannel); n != 1 {
		t.Fatalf("body rows %d, want 1", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM objects`); n != 0 {
		t.Fatalf("a body created %d objects", n)
	}
	e.mustReject(bucketObs(bucketChannel, 9), body, projection.RejectWrongCarrier)
	e.mustReject(bodyObs(registryChannel, 9), body, projection.RejectWrongChannel)
}
