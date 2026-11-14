package log

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func bucketProjector(t *testing.T) *Projector {
	t.Helper()
	return newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpBucketCreate: insertBucketApplier()})
}

func TestProjectFromOpInsertsLogAndProjectsAtomically(t *testing.T) {
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	header := formatted(t, bucketCreateOp(testJobID, 3600))
	obs := observed(1, header)
	obs.IsDocument = false
	obs.ActorUserID = 777

	outcome, err := projector.ProjectFromOp(context.Background(), db, obs)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if outcome.Kind != projection.OutcomeApplied || outcome.OpType != wire.OpBucketCreate {
		t.Fatalf("outcome = %+v, want applied bcreate", outcome)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log WHERE channel_id = ? AND msg_id = 1`, testChannel); got != 1 {
		t.Fatalf("replay_log rows = %d, want 1", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM buckets WHERE channel_id = ?`, testChannel); got != 1 {
		t.Fatalf("bucket rows = %d, want 1", got)
	}
	if got := rowText(t, db, `SELECT first_seen_hash FROM replay_log WHERE msg_id = 1`); got != HashHeader(header) {
		t.Fatalf("first_seen_hash = %q, want hash of the header", got)
	}
	if got := rowText(t, db, `SELECT raw_header FROM replay_log WHERE msg_id = 1`); got != header {
		t.Fatalf("raw_header = %q, want %q", got, header)
	}
	if got := rowText(t, db, `SELECT job_id || '/' || actor_user_id || '/' || msg_date || '/' || is_document FROM replay_log WHERE msg_id = 1`); got != testJobID+"/777/1763000060/0" {
		t.Fatalf("replay_log bookkeeping = %q", got)
	}
	if got := rowText(t, db, `SELECT outcome FROM projection_operations WHERE op_id = ? AND op_type = 'bcreate'`, testJobID); got != "applied" {
		t.Fatalf("operation outcome = %q, want applied", got)
	}
}

func TestProjectFromOpRollsBackLogWhenApplyFails(t *testing.T) {
	db := openProjectionDB(t)
	boom := errors.New("disk on fire")
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{
		wire.OpBucketCreate: applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
			return projection.ApplyResult{}, boom
		}),
	})

	_, err := projector.ProjectFromOp(context.Background(), db, observed(1, formatted(t, bucketCreateOp(testJobID, 3600))))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the applier error", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log`); got != 0 {
		t.Fatalf("replay_log rows = %d after a fatal apply error, want 0", got)
	}
}

func TestProjectFromOpIsIdempotentSameHash(t *testing.T) {
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	text := formatted(t, bucketCreateOp(testJobID, 3600))

	if _, err := projector.ProjectFromOp(context.Background(), db, observed(1, text)); err != nil {
		t.Fatalf("first: %v", err)
	}
	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(1, text))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if outcome.Kind != projection.OutcomeAlreadySeen || outcome.OpType != wire.OpBucketCreate {
		t.Fatalf("outcome = %+v, want already_seen bcreate", outcome)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log WHERE channel_id = ? AND msg_id = 1`, testChannel); got != 1 {
		t.Fatalf("replay_log rows = %d, want 1", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_tamper`); got != 0 {
		t.Fatalf("tamper rows = %d, want 0 for the same hash", got)
	}
}

func TestProjectFromOpDetectsTamper(t *testing.T) {
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	original := formatted(t, bucketCreateOp(testJobID, 3600))
	edited := formatted(t, bucketCreateOp(testJobID, 86400))

	if _, err := projector.ProjectFromOp(context.Background(), db, observed(1, original)); err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 1; i <= 2; i++ {
		outcome, err := projector.ProjectFromOp(context.Background(), db, observed(1, edited))
		if err != nil {
			t.Fatalf("edited observation %d: %v", i, err)
		}
		if outcome.Kind != projection.OutcomeTampered {
			t.Fatalf("outcome kind = %q, want tampered", outcome.Kind)
		}
		want := i
		if got := countRows(t, db, `SELECT observations FROM replay_log_tamper WHERE channel_id = ? AND msg_id = 1`, testChannel); got != want {
			t.Fatalf("observations = %d, want %d", got, want)
		}
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_tamper`); got != 1 {
		t.Fatalf("tamper rows = %d, want 1 per distinct new hash", got)
	}
	if got := rowText(t, db, `SELECT old_hash || '|' || new_hash || '|' || new_raw_header FROM replay_log_tamper`); got != HashHeader(original)+"|"+HashHeader(edited)+"|"+edited {
		t.Fatalf("tamper row = %q", got)
	}
	if got := rowText(t, db, `SELECT raw_header FROM replay_log WHERE msg_id = 1`); got != original {
		t.Fatalf("raw_header = %q, the first op must stay canonical", got)
	}
	if got := countRows(t, db, `SELECT cache_max_age_seconds FROM buckets WHERE channel_id = ?`, testChannel); got != 3600 {
		t.Fatalf("cache_max_age_seconds = %d, want 3600 from the original op", got)
	}
}

func TestProjectFromOpRecordsAndSkipsRejectedApplyError(t *testing.T) {
	db := openProjectionDB(t)
	rejecting := applyFunc(func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (?, ?, ?, 1, 0, 1)`,
			in.Observed.ChannelID, in.Observed.MsgID, in.Op.JobID); err != nil {
			return projection.ApplyResult{}, err
		}
		return projection.ApplyResult{}, &projection.Reject{Code: projection.RejectBadBodyRef, Detail: "bm is not below the put"}
	})
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{
		wire.OpPut:          rejecting,
		wire.OpBucketCreate: insertBucketApplier(),
	})
	rejectedText := formatted(t, putOp(otherJobID, "2026/cat.jpg"))

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(1, rejectedText))
	if err != nil {
		t.Fatalf("project rejected op: %v", err)
	}
	if outcome.Kind != projection.OutcomeRejected || outcome.RejectCode != projection.RejectBadBodyRef || outcome.Key != "2026/cat.jpg" {
		t.Fatalf("outcome = %+v, want rejected bad_body_ref for the key", outcome)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log WHERE msg_id = 1`); got != 1 {
		t.Fatalf("replay_log rows = %d, want 1", got)
	}
	if got := rowText(t, db, `SELECT code || '|' || op_type || '|' || job_id || '|' || raw_header || '|' || detail || '|' || msg_date FROM replay_log_rejects WHERE msg_id = 1`); got != "bad_body_ref|put|"+otherJobID+"|"+rejectedText+"|bm is not below the put|1763000060" {
		t.Fatalf("reject row = %q", got)
	}
	if got := rowText(t, db, `SELECT outcome FROM projection_operations WHERE op_id = ? AND op_type = 'put'`, otherJobID); got != "rejected" {
		t.Fatalf("operation outcome = %q, want rejected", got)
	}
	if got := rowText(t, db, `SELECT object_key FROM replay_log WHERE msg_id = 1`); got != "2026/cat.jpg" {
		t.Fatalf("object_key = %q, want the put key", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM object_bodies`); got != 0 {
		t.Fatalf("object_bodies rows = %d, a rejected apply must leave no partial writes", got)
	}

	next, err := projector.ProjectFromOp(context.Background(), db, observed(2, formatted(t, bucketCreateOp(testJobID, 3600))))
	if err != nil || next.Kind != projection.OutcomeApplied {
		t.Fatalf("replay after a reject: outcome %+v, error %v", next, err)
	}
}

func TestProjectFromOpDuplicateOpRejectWritesNoOperationRow(t *testing.T) {
	db := openProjectionDB(t)
	duplicate := applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
		return projection.ApplyResult{}, &projection.Reject{Code: projection.RejectDuplicateOp, Detail: "recorded at another message"}
	})
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpBucketCreate: duplicate})

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(5, formatted(t, bucketCreateOp(testJobID, 3600))))
	if err != nil || outcome.RejectCode != projection.RejectDuplicateOp {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM projection_operations`); got != 0 {
		t.Fatalf("projection_operations rows = %d, a duplicate_op reject writes none", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'duplicate_op'`); got != 1 {
		t.Fatalf("duplicate_op reject rows = %d, want 1", got)
	}
}

func TestProjectFromOpDuplicateOpRejectKeepsApplierWrites(t *testing.T) {
	db := openProjectionDB(t)
	duplicate := applyFunc(func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id) VALUES (?, ?, 'duplicate', 'k', ?)`,
			in.Observed.ChannelID, in.Observed.MsgID, in.Observed.MsgID); err != nil {
			return projection.ApplyResult{}, err
		}
		return projection.ApplyResult{}, &projection.Reject{Code: projection.RejectDuplicateOp, Detail: "recorded at another message"}
	})
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpPut: duplicate})

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(9, formatted(t, putOp(testJobID, "k"))))
	if err != nil || outcome.RejectCode != projection.RejectDuplicateOp {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
	if got := rowText(t, db, `SELECT reason || '|' || queued_by_msg_id FROM pending_message_cleanup WHERE msg_id = 9`); got != "duplicate|9" {
		t.Fatalf("cleanup row = %q, want the applier's duplicate row kept", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM projection_operations`); got != 0 {
		t.Fatalf("projection_operations rows = %d, a duplicate_op reject writes none", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'duplicate_op'`); got != 1 {
		t.Fatalf("duplicate_op reject rows = %d, want 1", got)
	}
}

func TestProjectFromOpOtherRejectRollsBackApplierWrites(t *testing.T) {
	db := openProjectionDB(t)
	rejecting := applyFunc(func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id) VALUES (?, ?, 'duplicate', 'k', ?)`,
			in.Observed.ChannelID, in.Observed.MsgID, in.Observed.MsgID); err != nil {
			return projection.ApplyResult{}, err
		}
		return projection.ApplyResult{}, &projection.Reject{Code: projection.RejectBadBodyRef, Detail: "bm is not below the put"}
	})
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpPut: rejecting})

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(9, formatted(t, putOp(testJobID, "k"))))
	if err != nil || outcome.RejectCode != projection.RejectBadBodyRef {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM pending_message_cleanup`); got != 0 {
		t.Fatalf("pending_message_cleanup rows = %d, a non-duplicate reject must roll back", got)
	}
}

func TestProjectFromOpWrapsRejectsFromTheApplier(t *testing.T) {
	db := openProjectionDB(t)
	wrapped := applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
		return projection.ApplyResult{}, errors.Join(errors.New("context"), &projection.Reject{Code: projection.RejectBucketNotFound, Detail: "unknown channel"})
	})
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{wire.OpBucketCreate: wrapped})

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(1, formatted(t, bucketCreateOp(testJobID, 3600))))
	if err != nil || outcome.RejectCode != projection.RejectBucketNotFound {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
}

func TestProjectFromOpTxAtomicWithCallerCheckpoint(t *testing.T) {
	db := openProjectionDB(t)
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{
		wire.OpBucketCreate: insertBucketApplier(),
		wire.OpPut: applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
			return projection.ApplyResult{Kind: projection.OutcomeApplied}, nil
		}),
	})
	text := formatted(t, bucketCreateOp(testJobID, 3600))
	checkpoint := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO channels (channel_id, kind, last_synced_msg, pts) VALUES (?, 'bucket', 1, 9)`, testChannel)
		return err
	}

	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		outcome, err := projector.ProjectFromOpTx(context.Background(), tx, observed(1, text))
		if err != nil {
			return err
		}
		if outcome.Kind != projection.OutcomeApplied {
			t.Fatalf("outcome = %+v, want applied", outcome)
		}
		return checkpoint(context.Background(), tx)
	})
	if err != nil {
		t.Fatalf("commit page: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM buckets`); got != 1 {
		t.Fatalf("bucket rows = %d after commit, want 1", got)
	}
	if got := countRows(t, db, `SELECT pts FROM channels WHERE channel_id = ?`, testChannel); got != 9 {
		t.Fatalf("checkpoint pts = %d, want 9", got)
	}

	failed := errors.New("watermark write failed")
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := projector.ProjectFromOpTx(context.Background(), tx, observed(2, formatted(t, putOp(otherJobID, "a.txt")))); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("error = %v, want the caller error", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log WHERE msg_id = 2`); got != 0 {
		t.Fatalf("replay_log rows = %d after caller rollback, want 0", got)
	}
}

func TestProjectFromOpClassification(t *testing.T) {
	tooLong := "TDX1|t=put|k=" + strings.Repeat("a", wire.MaxTextChars)
	tests := []struct {
		name         string
		text         string
		wantKind     projection.OutcomeKind
		wantCode     projection.RejectCode
		wantLogRows  int
		wantRejectOp string
	}{
		{name: "not a butterdb message", text: "hello there", wantKind: projection.OutcomeIgnored},
		{name: "service message with empty text", text: "", wantKind: projection.OutcomeIgnored},
		{name: "prefix lookalike without separator", text: "TDX1", wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "unsupported version", text: "TDX10|t=put", wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "pair without equals", text: "TDX1|t", wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "empty op type", text: "TDX1|t=|j=x", wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "known op with bad fields", text: "TDX1|t=bcreate|j=nope", wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "header too long", text: tooLong, wantKind: projection.OutcomeRejected, wantCode: projection.RejectMalformed},
		{name: "unknown op keeps the log row", text: "TDX1|t=zap|j=" + testJobID, wantKind: projection.OutcomeRejected, wantCode: projection.RejectUnknownOp, wantLogRows: 1, wantRejectOp: "zap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openProjectionDB(t)
			projector := bucketProjector(t)

			outcome, err := projector.ProjectFromOp(context.Background(), db, observed(7, tt.text))
			if err != nil {
				t.Fatalf("project: %v", err)
			}
			if outcome.Kind != tt.wantKind || outcome.RejectCode != tt.wantCode {
				t.Fatalf("outcome = %+v, want kind %q code %q", outcome, tt.wantKind, tt.wantCode)
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log`); got != tt.wantLogRows {
				t.Fatalf("replay_log rows = %d, want %d", got, tt.wantLogRows)
			}
			wantRejects := 0
			if tt.wantCode != "" {
				wantRejects = 1
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_rejects`); got != wantRejects {
				t.Fatalf("reject rows = %d, want %d", got, wantRejects)
			}
			if tt.wantRejectOp != "" {
				if got := rowText(t, db, `SELECT op_type FROM replay_log_rejects`); got != tt.wantRejectOp {
					t.Fatalf("reject op_type = %q, want %q", got, tt.wantRejectOp)
				}
			}
		})
	}
}

func TestMalformedRejectKeepsRawHeaderAndRepeatsIdempotently(t *testing.T) {
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	text := "TDX10|t=put\nsecond line is ignored"

	for i := 0; i < 2; i++ {
		if _, err := projector.ProjectFromOp(context.Background(), db, observed(3, text)); err != nil {
			t.Fatalf("observation %d: %v", i, err)
		}
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log_rejects WHERE msg_id = 3`); got != 1 {
		t.Fatalf("reject rows = %d, want 1", got)
	}
	if got := rowText(t, db, `SELECT raw_header FROM replay_log_rejects WHERE msg_id = 3`); got != "TDX10|t=put" {
		t.Fatalf("raw_header = %q, want the first line", got)
	}
}

func TestUnknownOpRecordedSkippable(t *testing.T) {
	db := openProjectionDB(t)
	called := false
	projector := newTestProjector(t, map[wire.OpType]projection.Applier{
		wire.OpBucketCreate: applyFunc(func(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
			called = true
			return insertBucketApplier().Apply(ctx, tx, in)
		}),
	})
	unknown := "TDX1|t=zap|j=" + testJobID + "|x=1"

	outcome, err := projector.ProjectFromOp(context.Background(), db, observed(10, unknown))
	if err != nil {
		t.Fatalf("project unknown op: %v", err)
	}
	if outcome.Kind != projection.OutcomeRejected || outcome.RejectCode != projection.RejectUnknownOp || outcome.OpType != "zap" {
		t.Fatalf("outcome = %+v, want rejected unknown_op for zap", outcome)
	}
	if got := rowText(t, db, `SELECT op_type || '|' || op_payload_json || '|' || raw_header || '|' || job_id || '|' || ifnull(object_key, '<null>') FROM replay_log WHERE msg_id = 10`); got != `zap|{"t":"zap"}|`+unknown+"||<null>" {
		t.Fatalf("replay_log row = %q", got)
	}
	if got := rowText(t, db, `SELECT code || '|' || raw_header FROM replay_log_rejects WHERE msg_id = 10`); got != "unknown_op|"+unknown {
		t.Fatalf("reject row = %q", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM projection_operations`); got != 0 {
		t.Fatalf("projection_operations rows = %d, an unknown op records none", got)
	}

	again, err := projector.ProjectFromOp(context.Background(), db, observed(10, unknown))
	if err != nil || again.Kind != projection.OutcomeAlreadySeen {
		t.Fatalf("second observation: outcome %+v, error %v", again, err)
	}
	next, err := projector.ProjectFromOp(context.Background(), db, observed(11, formatted(t, bucketCreateOp(testJobID, 3600))))
	if err != nil || next.Kind != projection.OutcomeApplied || !called {
		t.Fatalf("the page must continue after an unknown op: outcome %+v, error %v, applier called %v", next, err, called)
	}
}

func TestProjectFromOpStoresDocumentCarrierAndFirstLineOnly(t *testing.T) {
	db := openProjectionDB(t)
	projector := bucketProjector(t)
	header := formatted(t, bucketCreateOp(testJobID, 3600))
	obs := observed(4, "  "+header+"  \nextra text")
	obs.IsDocument = true

	if _, err := projector.ProjectFromOp(context.Background(), db, obs); err != nil {
		t.Fatalf("project: %v", err)
	}
	if got := rowText(t, db, `SELECT raw_header || '|' || is_document FROM replay_log WHERE msg_id = 4`); got != header+"|1" {
		t.Fatalf("replay_log row = %q, want the trimmed first line and is_document 1", got)
	}
}

func TestProjectFromOpFatalErrors(t *testing.T) {
	boom := errors.New("constraint failed")
	tests := []struct {
		name     string
		appliers map[wire.OpType]projection.Applier
		obs      projection.Observed
		wantErr  error
	}{
		{
			name:     "no applier registered for a known op",
			appliers: map[wire.OpType]projection.Applier{},
			obs:      observed(1, formatted(t, bucketCreateOp(testJobID, 3600))),
		},
		{
			name: "applier returns an unexpected outcome kind",
			appliers: map[wire.OpType]projection.Applier{
				wire.OpBucketCreate: applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
					return projection.ApplyResult{Kind: projection.OutcomeIgnored}, nil
				}),
			},
			obs: observed(1, formatted(t, bucketCreateOp(testJobID, 3600))),
		},
		{
			name: "applier returns a database error",
			appliers: map[wire.OpType]projection.Applier{
				wire.OpBucketCreate: applyFunc(func(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
					return projection.ApplyResult{}, boom
				}),
			},
			obs:     observed(1, formatted(t, bucketCreateOp(testJobID, 3600))),
			wantErr: boom,
		},
		{name: "zero message id", appliers: map[wire.OpType]projection.Applier{}, obs: observed(0, "hello")},
		{name: "zero channel id", appliers: map[wire.OpType]projection.Applier{}, obs: projection.Observed{MsgID: 1, RawText: "hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openProjectionDB(t)
			projector := newTestProjector(t, tt.appliers)

			_, err := projector.ProjectFromOp(context.Background(), db, tt.obs)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM replay_log`); got != 0 {
				t.Fatalf("replay_log rows = %d after a fatal error, want 0", got)
			}
		})
	}
}

func TestNewProjectorRejectsNilDependencies(t *testing.T) {
	registry := fakeRegistry{}
	tests := []struct {
		name string
		deps Dependencies
	}{
		{name: "nil registry", deps: Dependencies{Logger: discardLogger()}},
		{name: "nil logger", deps: Dependencies{Registry: registry}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewProjector(tt.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestProjectFromOpRejectsNilWriterAndTx(t *testing.T) {
	projector := bucketProjector(t)
	if _, err := projector.ProjectFromOp(context.Background(), nil, observed(1, "x")); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil writer error = %v, want ErrMissingDependency", err)
	}
	if _, err := projector.ProjectFromOpTx(context.Background(), nil, observed(1, "x")); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil tx error = %v, want ErrMissingDependency", err)
	}
}

func TestHashHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "empty", header: "", want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "abc", header: "abc", want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HashHeader(tt.header); got != tt.want {
				t.Fatalf("HashHeader(%q) = %s, want %s", tt.header, got, tt.want)
			}
		})
	}
}
