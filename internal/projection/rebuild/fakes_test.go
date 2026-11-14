package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type applied struct {
	channelID int64
	msgID     int64
	opType    wire.OpType
}

type fakeRegistry struct {
	appliers map[wire.OpType]projection.Applier
	calls    []applied
	failMsg  int64
	skipType wire.OpType
}

func newFakeRegistry() *fakeRegistry {
	r := &fakeRegistry{appliers: map[wire.OpType]projection.Applier{}}
	r.appliers[wire.OpBucketCreate] = r.wrap(bucketCreateApplier{})
	r.appliers[wire.OpPut] = r.wrap(putApplier{})
	r.appliers[wire.OpDel] = r.wrap(delApplier{})
	r.appliers[wire.OpBody] = r.wrap(bodyApplier{})
	r.appliers[wire.OpEncConfig] = r.wrap(encfgApplier{})
	return r
}

func (r *fakeRegistry) Lookup(t wire.OpType) (projection.Applier, bool) {
	if t == r.skipType {
		return nil, false
	}
	a, ok := r.appliers[t]
	return a, ok
}

func (r *fakeRegistry) wrap(inner projection.Applier) projection.Applier {
	return recordingApplier{registry: r, inner: inner}
}

type recordingApplier struct {
	registry *fakeRegistry
	inner    projection.Applier
}

var errInjected = errors.New("injected applier failure")

func (a recordingApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	a.registry.calls = append(a.registry.calls, applied{in.Observed.ChannelID, in.Observed.MsgID, in.Op.Type})
	if a.registry.failMsg != 0 && in.Observed.MsgID == a.registry.failMsg {
		return projection.ApplyResult{}, errInjected
	}
	return a.inner.Apply(ctx, tx, in)
}

func (r *fakeRegistry) reset() {
	r.calls = nil
}

func reject(code projection.RejectCode) error {
	return &projection.Reject{Code: code, Detail: "fake"}
}

func bucketLive(ctx context.Context, tx projection.Tx, channelID int64) (bool, error) {
	var deleted int64
	err := tx.QueryRowContext(ctx, `SELECT deleted_msg_id FROM buckets WHERE channel_id = ?`, channelID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return deleted == 0, err
}

func recordOutcome(ctx context.Context, tx projection.Tx, in projection.ApplyInput, outcome string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (?, ?, ?, ?, ?)
	`, in.Observed.ChannelID, in.Op.JobID, string(in.Op.Type), in.Observed.MsgID, outcome)
	return err
}

func duplicateAtOtherMsg(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (bool, error) {
	var msgID int64
	err := tx.QueryRowContext(ctx, `SELECT msg_id FROM projection_operations WHERE channel_id = ? AND op_id = ? AND op_type = ?`,
		in.Observed.ChannelID, in.Op.JobID, string(in.Op.Type)).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return msgID != in.Observed.MsgID, err
}

type bucketCreateApplier struct{}

func (bucketCreateApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	op := in.Op
	_, err := tx.ExecContext(ctx, `
		INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.BucketChannelID, op.Bucket, op.Policy, op.DefaultEncryption, op.CacheMaxAge, in.Observed.MsgID, op.JobID, in.Observed.MsgDate, in.Observed.MsgID)
	if err != nil {
		return projection.ApplyResult{}, reject(projection.RejectBucketExists)
	}
	return projection.ApplyResult{Kind: projection.OutcomeApplied}, recordOutcome(ctx, tx, in, "applied")
}

type bodyApplier struct{}

func (bodyApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if !in.Observed.IsDocument {
		return projection.ApplyResult{}, reject(projection.RejectWrongCarrier)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, encryption_version, msg_date)
		VALUES (?, ?, ?, ?, 0, 0, ?)
	`, in.Observed.ChannelID, in.Observed.MsgID, in.Op.JobID, in.Op.StoredSize, in.Observed.MsgDate)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	return projection.ApplyResult{Kind: projection.OutcomeApplied}, recordOutcome(ctx, tx, in, "applied")
}

type encfgApplier struct{}

func (encfgApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	op := in.Op
	live, err := bucketLive(ctx, tx, op.BucketChannelID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if !live {
		return projection.ApplyResult{}, reject(projection.RejectBucketNotFound)
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT config_version FROM encryption_keys WHERE bucket_channel_id = ? AND key_id = ?`, op.BucketChannelID, op.KeyID).Scan(&version)
	if err == nil && version >= op.ConfigVersion {
		return projection.ApplyResult{}, reject(projection.RejectDuplicateOp)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return projection.ApplyResult{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO encryption_keys (bucket_channel_id, key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id, job_id, msg_date)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.BucketChannelID, op.KeyID, op.WrappedKey, op.KeyCheck, op.KEKFingerprint, op.ConfigVersion, in.Observed.MsgID, in.Observed.MsgID, op.JobID, in.Observed.MsgDate)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	return projection.ApplyResult{Kind: projection.OutcomeApplied}, recordOutcome(ctx, tx, in, "applied")
}

type putApplier struct{}

func (putApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	op := in.Op
	channelID := in.Observed.ChannelID
	msgID := in.Observed.MsgID
	if live, err := bucketLive(ctx, tx, channelID); err != nil || !live {
		return projection.ApplyResult{}, orReject(err, projection.RejectBucketNotFound)
	}
	if op.BodyMsgID >= msgID {
		return projection.ApplyResult{}, reject(projection.RejectBadBodyRef)
	}
	if dup, err := duplicateAtOtherMsg(ctx, tx, in); err != nil || dup {
		return projection.ApplyResult{}, orReject(err, projection.RejectDuplicateOp)
	}
	var tombstone, existing int64
	if err := scanOptional(ctx, tx, &tombstone, `SELECT del_msg_id FROM object_tombstones WHERE channel_id = ? AND object_key = ?`, channelID, op.Key); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := scanOptional(ctx, tx, &existing, `SELECT put_msg_id FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, op.Key); err != nil {
		return projection.ApplyResult{}, err
	}
	if tombstone > msgID || existing > msgID {
		if err := queueCleanup(ctx, tx, channelID, msgID, op.Key, "superseded", msgID); err != nil {
			return projection.ApplyResult{}, err
		}
		return projection.ApplyResult{Kind: projection.OutcomeSuperseded}, recordOutcome(ctx, tx, in, "superseded")
	}
	if existing != 0 {
		if err := removeObject(ctx, tx, channelID, op.Key); err != nil {
			return projection.ApplyResult{}, err
		}
		if err := queueCleanup(ctx, tx, channelID, existing, op.Key, "superseded", msgID); err != nil {
			return projection.ApplyResult{}, err
		}
	}
	return projection.ApplyResult{Kind: projection.OutcomeApplied}, insertObject(ctx, tx, in)
}

func insertObject(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	op := in.Op
	_, err := tx.ExecContext(ctx, `
		INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256, content_type, writer, op_ts, msg_date, encrypted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
	`, in.Observed.ChannelID, op.Key, in.Observed.MsgID, op.BodyMsgID, op.JobID, op.StoredSize, op.StoredSize, op.ETag, op.SHA256, op.ContentType, op.Writer, op.Timestamp, in.Observed.MsgDate)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE buckets SET object_count = object_count + 1, stored_bytes = stored_bytes + ?1, plaintext_bytes = plaintext_bytes + ?1 WHERE channel_id = ?2
	`, op.StoredSize, in.Observed.ChannelID)
	if err != nil {
		return err
	}
	return recordOutcome(ctx, tx, in, "applied")
}

func removeObject(ctx context.Context, tx projection.Tx, channelID int64, key string) error {
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT stored_size FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key).Scan(&size); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE buckets SET object_count = object_count - 1, stored_bytes = stored_bytes - ?1, plaintext_bytes = plaintext_bytes - ?1 WHERE channel_id = ?2
	`, size, channelID)
	return err
}

type delApplier struct{}

func (delApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	op := in.Op
	channelID := in.Observed.ChannelID
	msgID := in.Observed.MsgID
	if live, err := bucketLive(ctx, tx, channelID); err != nil || !live {
		return projection.ApplyResult{}, orReject(err, projection.RejectBucketNotFound)
	}
	if dup, err := duplicateAtOtherMsg(ctx, tx, in); err != nil || dup {
		return projection.ApplyResult{}, orReject(err, projection.RejectDuplicateOp)
	}
	var tombstone, existing int64
	if err := scanOptional(ctx, tx, &tombstone, `SELECT del_msg_id FROM object_tombstones WHERE channel_id = ? AND object_key = ?`, channelID, op.Key); err != nil {
		return projection.ApplyResult{}, err
	}
	if tombstone < msgID {
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, target_put_msg_id, writer, msg_date)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, channelID, op.Key, msgID, op.JobID, op.PutVersion, op.Writer, in.Observed.MsgDate)
		if err != nil {
			return projection.ApplyResult{}, err
		}
	}
	if err := scanOptional(ctx, tx, &existing, `SELECT put_msg_id FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, op.Key); err != nil {
		return projection.ApplyResult{}, err
	}
	if existing != 0 && existing < msgID {
		if err := removeObject(ctx, tx, channelID, op.Key); err != nil {
			return projection.ApplyResult{}, err
		}
		if err := queueCleanup(ctx, tx, channelID, existing, op.Key, "deleted", msgID); err != nil {
			return projection.ApplyResult{}, err
		}
	}
	return projection.ApplyResult{Kind: projection.OutcomeApplied}, recordOutcome(ctx, tx, in, "applied")
}

func orReject(err error, code projection.RejectCode) error {
	if err != nil {
		return err
	}
	return reject(code)
}

func scanOptional(ctx context.Context, tx projection.Tx, dest *int64, query string, args ...any) error {
	err := tx.QueryRowContext(ctx, query, args...).Scan(dest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func queueCleanup(ctx context.Context, tx projection.Tx, channelID, msgID int64, key, reason string, queuedBy int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id)
		SELECT ?1, ?2, ?3, ?4, ?5 WHERE NOT EXISTS (SELECT 1 FROM deleted_messages WHERE channel_id = ?1 AND msg_id = ?2)
	`, channelID, msgID, reason, key, queuedBy)
	if err != nil {
		return fmt.Errorf("fake queue cleanup: %w", err)
	}
	return nil
}
