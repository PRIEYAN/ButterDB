package apply

import (
	"context"
	"database/sql"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type encConfigApplier struct {
	generation KeyGeneration
}

type keyRow struct {
	ConfigVersion int
	MsgID         int64
}

func (a encConfigApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := requireLiveBucketChannel(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := requireFirstOperation(ctx, tx, in.Observed.ChannelID, in); err != nil {
		return projection.ApplyResult{}, err
	}
	existing, found, err := loadKey(ctx, tx, in.Op.BucketChannelID, in.Op.KeyID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if found && existing.MsgID == in.Observed.MsgID {
		return applied(), nil
	}
	if err := requireMarkerFingerprint(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if found && in.Op.ConfigVersion <= existing.ConfigVersion {
		return projection.ApplyResult{}, rejectf(projection.RejectDuplicateOp, "key %s is already stored at config version %d", in.Op.KeyID, existing.ConfigVersion)
	}
	if err := storeKey(ctx, tx, in, found); err != nil {
		return projection.ApplyResult{}, err
	}
	a.generation.Advance(in.Op.BucketChannelID)
	return applied(), nil
}

func requireLiveBucketChannel(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	row, found, err := loadBucket(ctx, tx, in.Op.BucketChannelID)
	if err != nil {
		return err
	}
	if !found || row.DeletedMsgID != 0 {
		return rejectf(projection.RejectBucketNotFound, "no live bucket for channel %d", in.Op.BucketChannelID)
	}
	return nil
}

func loadKey(ctx context.Context, tx projection.Tx, bucketChannelID int64, keyID string) (keyRow, bool, error) {
	var row keyRow
	err := tx.QueryRowContext(ctx,
		`SELECT config_version, msg_id FROM encryption_keys WHERE bucket_channel_id = ?1 AND key_id = ?2`,
		bucketChannelID, keyID).Scan(&row.ConfigVersion, &row.MsgID)
	if errors.Is(err, sql.ErrNoRows) {
		return keyRow{}, false, nil
	}
	if err != nil {
		return keyRow{}, false, sqlFailure("load encryption key", err)
	}
	return row, true, nil
}

func requireMarkerFingerprint(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	var fingerprint string
	err := tx.QueryRowContext(ctx,
		`SELECT kek_fingerprint FROM registry_markers WHERE channel_id = ?1 AND msg_id < ?2 ORDER BY msg_id DESC LIMIT 1`,
		in.Observed.ChannelID, in.Observed.MsgID).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return sqlFailure("load registry marker fingerprint", err)
	}
	if fingerprint != in.Op.KEKFingerprint {
		return rejectf(projection.RejectKEKMismatch, "key fingerprint differs from the registry marker in effect")
	}
	return nil
}

func storeKey(ctx context.Context, tx projection.Tx, in projection.ApplyInput, exists bool) error {
	op := in.Op
	obs := in.Observed
	if exists {
		_, err := tx.ExecContext(ctx,
			`UPDATE encryption_keys SET wrapped_dek = ?3, key_check = ?4, kek_fingerprint = ?5, config_version = ?6, msg_id = ?7, job_id = ?8, msg_date = ?9
WHERE bucket_channel_id = ?1 AND key_id = ?2`,
			op.BucketChannelID, op.KeyID, op.WrappedKey, op.KeyCheck, op.KEKFingerprint, op.ConfigVersion, obs.MsgID, op.JobID, obs.MsgDate)
		if err != nil {
			return sqlFailure("update encryption key", err)
		}
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO encryption_keys (bucket_channel_id, key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id, job_id, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?7, ?8, ?9)`,
		op.BucketChannelID, op.KeyID, op.WrappedKey, op.KeyCheck, op.KEKFingerprint, op.ConfigVersion, obs.MsgID, op.JobID, obs.MsgDate)
	if err != nil {
		return sqlFailure("insert encryption key", err)
	}
	return nil
}
