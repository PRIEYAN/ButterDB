package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func (e *env) seedBucket(name string, channelID int64, msgID int64) {
	e.t.Helper()
	e.project(registryChannel, msgID, headerOf(e.t, bucketCreateOp(int(msgID), name, channelID)), false)
}

func (e *env) objectPut(channelID int64, key string) int64 {
	e.t.Helper()
	var msgID int64
	err := e.db.Reader().QueryRow(`SELECT put_msg_id FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		e.t.Fatalf("object %s: %v", key, err)
	}
	return msgID
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		deps Dependencies
	}{
		{"nil registry", Dependencies{Logger: logger}},
		{"nil logger", Dependencies{Registry: newFakeRegistry()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New() error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestRebuildEmptyChannelClears(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 5, headerOf(t, putOp(5, "stray", 0, 0)), false)
	e.exec(`DELETE FROM replay_log WHERE channel_id = ?`, bucketChannel)

	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, bucketChannel); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?`, bucketChannel); n != 0 {
		t.Fatalf("objects after rebuild of empty replay = %d, want 0", n)
	}
	if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?`, bucketChannel); n != 0 {
		t.Fatalf("object_count = %d, want 0", n)
	}
}

func TestRebuildReplaysAscending(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	for _, msgID := range []int64{30, 10, 20} {
		e.appendReplay(bucketChannel, msgID, headerOf(t, putOp(int(msgID), "key", 0, 0)), false)
	}
	e.applier.reset()

	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, bucketChannel); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var order []int64
	for _, call := range e.applier.calls {
		order = append(order, call.msgID)
	}
	if want := []int64{10, 20, 30}; !reflect.DeepEqual(order, want) {
		t.Fatalf("apply order = %v, want %v", order, want)
	}
	if got := e.objectPut(bucketChannel, "key"); got != 30 {
		t.Fatalf("put_msg_id = %d, want 30 (highest msg_id wins)", got)
	}
}

func TestRebuildReplaysAscendingAcrossBatches(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	last := int64(rebuildReplayBatchSize)*2 + 7
	for msgID := int64(3); msgID <= last; msgID += 1 {
		e.appendReplay(bucketChannel, msgID*2, headerOf(t, putOp(int(msgID), "key", 0, 0)), false)
	}
	e.applier.reset()

	result, err := e.rebuild.RebuildChannel(context.Background(), e.db, bucketChannel)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if want := int(last) - 2; result.Applied != want {
		t.Fatalf("applied = %d, want %d", result.Applied, want)
	}
	previous := int64(0)
	for _, call := range e.applier.calls {
		if call.msgID <= previous {
			t.Fatalf("msg %d applied after %d", call.msgID, previous)
		}
		previous = call.msgID
	}
	if got := e.objectPut(bucketChannel, "key"); got != last*2 {
		t.Fatalf("put_msg_id = %d, want %d", got, last*2)
	}
}

func TestRebuildRollsBackAllBatchesOnReplayFailure(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 2, headerOf(t, putOp(2, "preexisting", 0, 0)), false)
	e.exec(`DELETE FROM replay_log WHERE channel_id = ? AND msg_id = 2`, bucketChannel)
	e.exec(`UPDATE objects SET content_type = 'sentinel' WHERE object_key = 'preexisting'`)
	for msgID := int64(10); msgID < 10+int64(rebuildReplayBatchSize)+5; msgID++ {
		e.appendReplay(bucketChannel, msgID, headerOf(t, putOp(int(msgID), fmt.Sprintf("key-%d", msgID), 0, 0)), false)
	}
	e.applier.failMsg = 10 + int64(rebuildReplayBatchSize) + 2

	_, err := e.rebuild.RebuildAll(context.Background(), e.db)
	if !errors.Is(err, errInjected) {
		t.Fatalf("rebuild error = %v, want injected failure", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE object_key = 'preexisting' AND content_type = 'sentinel'`); n != 1 {
		t.Fatal("failed rebuild did not roll back the original projection")
	}
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE object_key = 'key-10'`); n != 0 {
		t.Fatal("failed rebuild committed an earlier batch")
	}
	if n := e.count(`SELECT needs_projection_rebuild FROM channels WHERE channel_id = ?`, bucketChannel); n != 1 {
		t.Fatal("failed rebuild cleared the rebuild flag")
	}
}

func TestRebuildDifferentChannelsAreIsolated(t *testing.T) {
	e := newEnv(t)
	e.addChannel(otherChannel, "bucket", "active")
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.seedBucket("bucket-c", otherChannel, 2)
	e.project(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.project(otherChannel, 5, headerOf(t, putOp(6, "c", 0, 0)), false)
	e.exec(`UPDATE objects SET content_type = 'sentinel' WHERE channel_id = ?`, otherChannel)

	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, bucketChannel); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := e.objectPut(bucketChannel, "a"); got != 5 {
		t.Fatalf("rebuilt channel lost its object: put_msg_id %d", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ? AND content_type = 'sentinel'`, otherChannel); n != 1 {
		t.Fatal("rebuild of one channel touched another channel")
	}
	if n := e.count(`SELECT needs_projection_rebuild FROM channels WHERE channel_id = ?`, otherChannel); n != 1 {
		t.Fatal("rebuild cleared the flag of another channel")
	}
}

func TestRebuildHandlesTombstones(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "x.png", 0, 0)), false)
	e.appendReplay(bucketChannel, 8, headerOf(t, delOp(8, "x.png")), false)
	e.appendReplay(bucketChannel, 6, headerOf(t, putOp(6, "late.png", 0, 0)), false)
	e.appendReplay(bucketChannel, 9, headerOf(t, delOp(9, "late.png")), false)
	e.appendReplay(bucketChannel, 7, headerOf(t, putOp(7, "x.png", 0, 0)), false)

	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, bucketChannel); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := e.objectPut(bucketChannel, "x.png"); got != 0 {
		t.Fatalf("tombstoned key visible with put %d", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ?`, bucketChannel); n != 2 {
		t.Fatalf("tombstones = %d, want 2", n)
	}
	if n := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?`, bucketChannel); n != 0 {
		t.Fatalf("object_count = %d, want 0", n)
	}
}

func TestRebuildReplaysEncryptionConfig(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(registryChannel, 7, headerOf(t, encfgOp(7, bucketChannel)), false)
	e.exec(`DELETE FROM encryption_keys`)

	if _, err := e.rebuild.RebuildAll(context.Background(), e.db); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	var wrapped, check []byte
	var firstMsg int64
	err := e.db.Reader().QueryRow(`SELECT wrapped_dek, key_check, first_msg_id FROM encryption_keys WHERE bucket_channel_id = ?`, bucketChannel).Scan(&wrapped, &check, &firstMsg)
	if err != nil {
		t.Fatalf("encryption key row: %v", err)
	}
	if string(wrapped) != string(make72()) || string(check) != string(make61()) || firstMsg != 7 {
		t.Fatalf("encryption key mismatch: msg %d", firstMsg)
	}
}

func TestRebuildPreservesReplayLog(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.exec(`INSERT INTO replay_log_tamper (channel_id, msg_id, old_hash, new_hash, new_raw_header) VALUES (?, 5, ?, ?, 'TDX1|t=put|edited')`, bucketChannel, shaOf("a"), shaOf("b"))
	e.exec(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?, 4, 'external', 1)`, bucketChannel)
	e.exec(`INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, queued_by_msg_id) VALUES (?, 3, 'orphan_body', 0)`, bucketChannel)
	e.exec(`INSERT INTO pending_channel_cleanup (channel_id, bdel_msg_id) VALUES (?, 9)`, otherChannel)
	e.exec(`INSERT INTO replay_log_rejects (channel_id, msg_id, code, raw_header, msg_date) VALUES (?, 99, 'malformed', 'TDX1|garbage', 1)`, bucketChannel)
	before := e.snapshot("replay_log", "replay_log_tamper", "deleted_messages", "pending_message_cleanup", "pending_channel_cleanup")
	malformed := e.dump("replay_log_rejects")

	e.rebuildAll()

	assertSnapshotsEqual(t, before, e.snapshot("replay_log", "replay_log_tamper", "deleted_messages", "pending_message_cleanup", "pending_channel_cleanup"))
	if got := e.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE msg_id = 99 AND code = 'malformed'`); got != 1 {
		t.Fatalf("malformed reject rows = %d, want 1 (rows before: %v)", got, malformed)
	}
}

func TestRebuildRerecordsRejects(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.appendReplay(bucketChannel, 6, headerOf(t, putOp(6, "b", 9, 3)), false)
	e.appendReplay(bucketChannel, 7, "TDX1|t=future|j="+jobID(7), false)
	e.appendReplay(bucketChannel, 8, headerOf(t, bodyOp(8, 3)), false)
	e.exec(`INSERT INTO replay_log_rejects (channel_id, msg_id, code, raw_header, msg_date) VALUES (?, 5, 'size_mismatch', 'stale', 1)`, bucketChannel)

	result := e.rebuildAll()

	if result.Rejected != 3 {
		t.Fatalf("rejected = %d, want 3", result.Rejected)
	}
	want := map[int64]string{6: "bad_body_ref", 7: "unknown_op", 8: "wrong_carrier"}
	rows, err := e.db.Reader().Query(`SELECT msg_id, code FROM replay_log_rejects WHERE channel_id = ? ORDER BY msg_id`, bucketChannel)
	if err != nil {
		t.Fatalf("query rejects: %v", err)
	}
	defer rows.Close()
	got := map[int64]string{}
	for rows.Next() {
		var msgID int64
		var code string
		if err := rows.Scan(&msgID, &code); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[msgID] = code
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rejects = %v, want %v", got, want)
	}
	if n := e.count(`SELECT COUNT(*) FROM projection_operations WHERE channel_id = ? AND outcome = 'rejected' AND op_id = ?`, bucketChannel, jobID(6)); n != 1 {
		t.Fatalf("rejected outcome rows for bad put = %d, want 1", n)
	}
}

func TestRebuildAllRunsRegistryFirstAndSkipsDeletedChannels(t *testing.T) {
	e := newEnv(t)
	e.addChannel(otherChannel, "bucket", "deleted")
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.appendReplay(otherChannel, 5, headerOf(t, putOp(6, "gone", 0, 0)), false)
	e.applier.reset()

	e.rebuildAll()

	var channels []int64
	for _, call := range e.applier.calls {
		channels = append(channels, call.channelID)
	}
	if want := []int64{registryChannel, bucketChannel}; !reflect.DeepEqual(channels, want) {
		t.Fatalf("channels applied = %v, want %v", channels, want)
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE needs_projection_rebuild = 1`); n != 1 {
		t.Fatalf("channels still flagged = %d, want only the deleted one", n)
	}
}

func TestRebuildChannelRejectsRegistryAndUnknownChannels(t *testing.T) {
	e := newEnv(t)
	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, registryChannel); !errors.Is(err, ErrRegistryChannelAlone) {
		t.Fatalf("registry error = %v", err)
	}
	if _, err := e.rebuild.RebuildChannel(context.Background(), e.db, 424242); !errors.Is(err, errs.ErrNoSuchBucket) {
		t.Fatalf("unknown channel error = %v", err)
	}
	if _, err := e.rebuild.RebuildAll(context.Background(), nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil writer error = %v", err)
	}
}

func TestRebuildFailsWhenKnownOpHasNoApplier(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.applier.skipType = wire.OpPut

	if _, err := e.rebuild.RebuildAll(context.Background(), e.db); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("rebuild error = %v, want ErrMissingDependency", err)
	}
}

func TestRebuildFailsOnUnparseableLoggedHeader(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.exec(`UPDATE replay_log SET raw_header = 'TDX1|t=put|broken' WHERE channel_id = ? AND msg_id = 5`, bucketChannel)

	if _, err := e.rebuild.RebuildAll(context.Background(), e.db); !errors.Is(err, wire.ErrWireMalformed) {
		t.Fatalf("rebuild error = %v, want malformed", err)
	}
}

func TestRebuildRejectRollsBackPartialApplierWrites(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.applier.appliers[wire.OpPut] = partialWriteThenReject{}
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)

	e.rebuildAll()

	if n := e.count(`SELECT COUNT(*) FROM object_bodies WHERE channel_id = ?`, bucketChannel); n != 0 {
		t.Fatalf("partial write of a rejected op survived: %d rows", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE msg_id = 5 AND code = 'size_mismatch'`); n != 1 {
		t.Fatalf("reject rows = %d, want 1", n)
	}
}

type partialWriteThenReject struct{}

func (partialWriteThenReject) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (?, 1, ?, 1, 0, 0)`, in.Observed.ChannelID, in.Op.JobID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	return projection.ApplyResult{}, reject(projection.RejectSizeMismatch)
}

type writeThenRejectApplier struct {
	code projection.RejectCode
}

func (a writeThenRejectApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := queueCleanup(ctx, tx, in.Observed.ChannelID, in.Observed.MsgID, in.Op.Key, "duplicate", in.Observed.MsgID); err != nil {
		return projection.ApplyResult{}, err
	}
	return projection.ApplyResult{}, reject(a.code)
}

type fixedKindApplier struct {
	kind projection.OutcomeKind
}

func (a fixedKindApplier) Apply(context.Context, projection.Tx, projection.ApplyInput) (projection.ApplyResult, error) {
	return projection.ApplyResult{Kind: a.kind}, nil
}

func TestRebuildRejectWritesFollowRejectCode(t *testing.T) {
	cases := []struct {
		name        string
		code        projection.RejectCode
		wantCleanup int
	}{
		{"duplicate op keeps applier writes", projection.RejectDuplicateOp, 1},
		{"bad body ref rolls writes back", projection.RejectBadBodyRef, 0},
		{"size mismatch rolls writes back", projection.RejectSizeMismatch, 0},
		{"bucket not found rolls writes back", projection.RejectBucketNotFound, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedBucket("bucket-a", bucketChannel, 1)
			e.applier.appliers[wire.OpPut] = writeThenRejectApplier{code: tc.code}
			e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)

			result := e.rebuildAll()

			if result.Rejected != 1 {
				t.Fatalf("rejected = %d, want 1", result.Rejected)
			}
			if n := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ? AND msg_id = 5 AND reason = 'duplicate'`, bucketChannel); n != tc.wantCleanup {
				t.Fatalf("cleanup rows = %d, want %d", n, tc.wantCleanup)
			}
			if n := e.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE channel_id = ? AND msg_id = 5 AND code = ?`, bucketChannel, string(tc.code)); n != 1 {
				t.Fatalf("reject rows = %d, want 1", n)
			}
		})
	}
}

func TestRebuildFailsOnUnexpectedApplierOutcome(t *testing.T) {
	cases := []struct {
		name string
		kind projection.OutcomeKind
	}{
		{"rejected kind without reject error", projection.OutcomeRejected},
		{"ignored kind", projection.OutcomeIgnored},
		{"empty kind", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedBucket("bucket-a", bucketChannel, 1)
			e.applier.appliers[wire.OpPut] = fixedKindApplier{kind: tc.kind}
			e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)

			if _, err := e.rebuild.RebuildAll(context.Background(), e.db); !errors.Is(err, ErrUnexpectedOutcome) {
				t.Fatalf("rebuild error = %v, want ErrUnexpectedOutcome", err)
			}
		})
	}
}

func TestRebuildRecordsAppliedAndSupersededOutcomes(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.appendReplay(bucketChannel, 6, headerOf(t, putOp(6, "a", 0, 0)), false)
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
	e.appendReplay(bucketChannel, 7, headerOf(t, bodyOp(7, 3)), true)

	e.rebuildAll()

	cases := []struct {
		name    string
		opID    string
		opType  string
		msgID   int64
		outcome string
	}{
		{"bucket create", jobID(1), "bcreate", 1, "applied"},
		{"older put replays first and applies", jobID(5), "put", 5, "applied"},
		{"newer put replays last and applies", jobID(6), "put", 6, "applied"},
		{"body document", jobID(7), "body", 7, "applied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msgID int64
			var outcome string
			err := e.db.Reader().QueryRow(`SELECT msg_id, outcome FROM projection_operations WHERE op_id = ? AND op_type = ?`, tc.opID, tc.opType).Scan(&msgID, &outcome)
			if err != nil {
				t.Fatalf("outcome row: %v", err)
			}
			if msgID != tc.msgID || outcome != tc.outcome {
				t.Fatalf("outcome row = msg %d %s, want msg %d %s", msgID, outcome, tc.msgID, tc.outcome)
			}
		})
	}
}

func TestRebuildDuplicateJobPutStaysRejected(t *testing.T) {
	cases := []struct {
		name   string
		repair func(e *env)
	}{
		{"after rebuild all", func(e *env) { e.rebuildAll() }},
		{"after repair key", func(e *env) {
			if _, err := e.rebuild.RepairKey(context.Background(), e.db, bucketChannel, "a"); err != nil {
				t.Fatalf("repair key: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedBucket("bucket-a", bucketChannel, 1)
			e.project(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)
			e.project(bucketChannel, 6, headerOf(t, putOp(5, "a", 0, 0)), false)
			wantRejects := e.dump("replay_log_rejects")
			wantObjects := e.dump("objects")
			wantCleanup := e.dump("pending_message_cleanup")
			if e.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE msg_id = 6 AND code = 'duplicate_op'`) != 1 {
				t.Fatal("incremental projection did not reject the duplicate put")
			}

			tc.repair(e)

			if got := e.dump("replay_log_rejects"); !reflect.DeepEqual(got, wantRejects) {
				t.Fatalf("rejects = %v, want %v", got, wantRejects)
			}
			if got := e.dump("objects"); !reflect.DeepEqual(got, wantObjects) {
				t.Fatalf("objects = %v, want %v", got, wantObjects)
			}
			if got := e.dump("pending_message_cleanup"); !reflect.DeepEqual(got, wantCleanup) {
				t.Fatalf("cleanup = %v, want %v", got, wantCleanup)
			}
			if got := e.objectPut(bucketChannel, "a"); got != 5 {
				t.Fatalf("object a put_msg_id = %d, want 5", got)
			}
		})
	}
}

func TestRebuildUnknownOpRejectDetailMatchesProjector(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	header := "TDX1|t=future|j=" + jobID(7)
	e.appendReplay(bucketChannel, 7, header, false)
	e.exec(`
		INSERT INTO replay_log_rejects (channel_id, msg_id, code, op_type, job_id, raw_header, detail, msg_date)
		VALUES (?, 7, 'unknown_op', 'future', ?, ?, ?, 1007)
	`, bucketChannel, jobID(7), header, wire.ErrWireUnknownOp.Error())
	before := e.dump("replay_log_rejects")

	e.rebuildAll()

	var detail string
	if err := e.db.Reader().QueryRow(`SELECT detail FROM replay_log_rejects WHERE channel_id = ? AND msg_id = 7`, bucketChannel).Scan(&detail); err != nil {
		t.Fatalf("reject row: %v", err)
	}
	if detail != wire.ErrWireUnknownOp.Error() {
		t.Fatalf("detail = %q, want %q", detail, wire.ErrWireUnknownOp.Error())
	}
	if got := e.dump("replay_log_rejects"); len(got) != len(before) {
		t.Fatalf("reject rows = %d, want %d", len(got), len(before))
	}
}

func TestRebuildRecordsSupersededOutcome(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.applier.appliers[wire.OpPut] = fixedKindApplier{kind: projection.OutcomeSuperseded}
	e.appendReplay(bucketChannel, 5, headerOf(t, putOp(5, "a", 0, 0)), false)

	result := e.rebuildAll()

	if result.Applied != 2 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want 2 applied and 0 rejected", result)
	}
	if n := e.count(`SELECT COUNT(*) FROM projection_operations WHERE channel_id = ? AND op_id = ? AND op_type = 'put' AND msg_id = 5 AND outcome = 'superseded'`, bucketChannel, jobID(5)); n != 1 {
		t.Fatalf("superseded outcome rows = %d, want 1", n)
	}
}
