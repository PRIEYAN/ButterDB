package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type objectRow struct {
	PutMsgID      int64
	BodyMsgID     int64
	StoredSize    int64
	PlaintextSize int64
}

func loadObject(ctx context.Context, tx projection.Tx, channelID int64, key string) (objectRow, bool, error) {
	var row objectRow
	err := tx.QueryRowContext(ctx,
		`SELECT put_msg_id, body_msg_id, stored_size, plaintext_size FROM objects WHERE channel_id = ?1 AND object_key = ?2`,
		channelID, key).Scan(&row.PutMsgID, &row.BodyMsgID, &row.StoredSize, &row.PlaintextSize)
	if errors.Is(err, sql.ErrNoRows) {
		return objectRow{}, false, nil
	}
	if err != nil {
		return objectRow{}, false, sqlFailure("load object", err)
	}
	return row, true, nil
}

func loadTombstoneMsgID(ctx context.Context, tx projection.Tx, channelID int64, key string) (int64, error) {
	var msgID int64
	err := tx.QueryRowContext(ctx,
		`SELECT del_msg_id FROM object_tombstones WHERE channel_id = ?1 AND object_key = ?2`,
		channelID, key).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, sqlFailure("load tombstone", err)
	}
	return msgID, nil
}

func adjustBucketCounters(ctx context.Context, tx projection.Tx, channelID, objects, storedBytes, plaintextBytes int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE buckets SET object_count = object_count + ?2, stored_bytes = stored_bytes + ?3, plaintext_bytes = plaintext_bytes + ?4 WHERE channel_id = ?1`,
		channelID, objects, storedBytes, plaintextBytes)
	if err != nil {
		return sqlFailure("adjust bucket counters", err)
	}
	return nil
}

func removeObject(ctx context.Context, tx projection.Tx, channelID int64, key string, row objectRow) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM objects WHERE channel_id = ?1 AND object_key = ?2`, channelID, key); err != nil {
		return sqlFailure("delete object", err)
	}
	return adjustBucketCounters(ctx, tx, channelID, -1, -row.StoredSize, -row.PlaintextSize)
}

func metaJSON(meta map[string]string) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "", sqlFailure("encode metadata", err)
	}
	return string(encoded), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func insertObject(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	op := in.Op
	userMeta, err := metaJSON(op.UserMeta)
	if err != nil {
		return err
	}
	systemMeta, err := metaJSON(op.SystemMeta)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256,
  content_type, user_meta_json, system_meta_json, encrypted, encryption_version, key_id, writer, op_ts, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18)`,
		in.Observed.ChannelID, op.Key, in.Observed.MsgID, op.BodyMsgID, op.JobID, op.StoredSize, plaintextSizeOf(op), op.ETag, op.SHA256,
		op.ContentType, userMeta, systemMeta, boolInt(op.Encrypted), encryptionVersionOf(op), op.KeyID, op.Writer, op.Timestamp, in.Observed.MsgDate)
	if err != nil {
		return sqlFailure("insert object", err)
	}
	return adjustBucketCounters(ctx, tx, in.Observed.ChannelID, 1, op.StoredSize, plaintextSizeOf(op))
}

func plaintextSizeOf(op wire.Op) int64 {
	if op.Encrypted {
		return op.PlaintextSize
	}
	return op.StoredSize
}

func encryptionVersionOf(op wire.Op) int {
	if op.Encrypted {
		return op.EncryptionVersion
	}
	return 0
}
