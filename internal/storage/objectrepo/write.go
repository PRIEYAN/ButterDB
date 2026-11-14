package objectrepo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type Cleanup struct {
	ChannelID     int64
	MsgID         int64
	Reason        string
	ObjectKey     string
	QueuedByMsgID int64
}

const upsertObject = `INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size,
  etag, sha256, content_type, user_meta_json, system_meta_json, encrypted, encryption_version, key_id, writer, op_ts, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18)
ON CONFLICT (channel_id, object_key) DO UPDATE SET
  put_msg_id = excluded.put_msg_id, body_msg_id = excluded.body_msg_id, job_id = excluded.job_id,
  stored_size = excluded.stored_size, plaintext_size = excluded.plaintext_size, etag = excluded.etag,
  sha256 = excluded.sha256, content_type = excluded.content_type, user_meta_json = excluded.user_meta_json,
  system_meta_json = excluded.system_meta_json, encrypted = excluded.encrypted,
  encryption_version = excluded.encryption_version, key_id = excluded.key_id, writer = excluded.writer,
  op_ts = excluded.op_ts, msg_date = excluded.msg_date`

func PutObject(ctx context.Context, tx Execer, meta object.ObjectMeta, opTS int64) error {
	userJSON, err := encodeMeta(meta.UserMeta)
	if err != nil {
		return fmt.Errorf("objectrepo: put %q: user metadata: %w", meta.Key, err)
	}
	systemJSON, err := encodeMeta(meta.SystemMeta)
	if err != nil {
		return fmt.Errorf("objectrepo: put %q: system metadata: %w", meta.Key, err)
	}
	encrypted := 0
	if meta.Encrypted {
		encrypted = 1
	}
	_, err = tx.ExecContext(ctx, upsertObject, meta.ChannelID, meta.Key, meta.PutMsgID, meta.BodyMsgID, meta.JobID,
		meta.StoredSize, meta.PlaintextSize, meta.ETag, meta.SHA256, meta.ContentType, userJSON, systemJSON,
		encrypted, meta.EncryptionVersion, meta.KeyID, meta.Writer, opTS, meta.MsgDate)
	if err != nil {
		return fmt.Errorf("objectrepo: put %q: %w", meta.Key, err)
	}
	return nil
}

func DeleteObject(ctx context.Context, tx Execer, channelID int64, key string) (bool, error) {
	result, err := tx.ExecContext(ctx, "DELETE FROM objects WHERE channel_id = ?1 AND object_key = ?2", channelID, key)
	if err != nil {
		return false, fmt.Errorf("objectrepo: delete %q: %w", key, err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("objectrepo: delete %q: %w", key, err)
	}
	return removed > 0, nil
}

const queueCleanup = `INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id)
SELECT ?1, ?2, ?3, ?4, ?5
WHERE NOT EXISTS (SELECT 1 FROM objects WHERE channel_id = ?1 AND (put_msg_id = ?2 OR body_msg_id = ?2))
ON CONFLICT (channel_id, msg_id) DO NOTHING`

func QueueCleanup(ctx context.Context, tx Execer, item Cleanup) (bool, error) {
	var key any
	if item.ObjectKey != "" {
		key = item.ObjectKey
	}
	result, err := tx.ExecContext(ctx, queueCleanup, item.ChannelID, item.MsgID, item.Reason, key, item.QueuedByMsgID)
	if err != nil {
		return false, fmt.Errorf("objectrepo: queue cleanup of message %d: %w", item.MsgID, err)
	}
	queued, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("objectrepo: queue cleanup of message %d: %w", item.MsgID, err)
	}
	return queued > 0, nil
}
