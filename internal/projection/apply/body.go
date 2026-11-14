package apply

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type bodyApplier struct{}

func (bodyApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if _, err := requireBucketScope(ctx, tx, in, true); err != nil {
		return projection.ApplyResult{}, err
	}
	obs := in.Observed
	gone, err := isMessageDeleted(ctx, tx, obs.ChannelID, obs.MsgID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if gone {
		return applied(), nil
	}
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, encryption_version, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
		obs.ChannelID, obs.MsgID, in.Op.JobID, in.Op.StoredSize, boolInt(in.Op.Encrypted), encryptionVersionOf(in.Op), obs.MsgDate)
	if err != nil {
		return projection.ApplyResult{}, sqlFailure("insert object body", err)
	}
	return applied(), nil
}
