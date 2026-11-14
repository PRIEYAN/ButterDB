package apply

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type delApplier struct{}

func (delApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if _, err := requireBucketScope(ctx, tx, in, false); err != nil {
		return projection.ApplyResult{}, err
	}
	channelID := in.Observed.ChannelID
	if err := requireFirstOperation(ctx, tx, channelID, in); err != nil {
		return projection.ApplyResult{}, err
	}
	tombstone, err := loadTombstoneMsgID(ctx, tx, channelID, in.Op.Key)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if tombstone == in.Observed.MsgID {
		return applied(), nil
	}
	if tombstone > in.Observed.MsgID {
		return superseded(), nil
	}
	if err := upsertTombstone(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := removeVersionsBelowTombstone(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	return applied(), nil
}

func upsertTombstone(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	obs := in.Observed
	_, err := tx.ExecContext(ctx,
		`INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, target_put_msg_id, writer, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
ON CONFLICT (channel_id, object_key) DO UPDATE SET
  del_msg_id = excluded.del_msg_id, job_id = excluded.job_id, target_put_msg_id = excluded.target_put_msg_id,
  writer = excluded.writer, msg_date = excluded.msg_date`,
		obs.ChannelID, in.Op.Key, obs.MsgID, in.Op.JobID, in.Op.PutVersion, in.Op.Writer, obs.MsgDate)
	if err != nil {
		return sqlFailure("upsert tombstone", err)
	}
	return nil
}

func removeVersionsBelowTombstone(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	obs := in.Observed
	current, found, err := loadObject(ctx, tx, obs.ChannelID, in.Op.Key)
	if err != nil || !found || current.PutMsgID > obs.MsgID {
		return err
	}
	if err := removeObject(ctx, tx, obs.ChannelID, in.Op.Key, current); err != nil {
		return err
	}
	return queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, cleanupDeleted, current.PutMsgID, current.BodyMsgID)
}
