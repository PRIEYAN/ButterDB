package apply

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type bucketConfigApplier struct{}

func (bucketConfigApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	row, err := requireNamedBucket(ctx, tx, in)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if err := requireFirstOperation(ctx, tx, in.Observed.ChannelID, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if in.Observed.MsgID == row.ConfigMsgID {
		return applied(), nil
	}
	if in.Observed.MsgID < row.ConfigMsgID {
		return superseded(), nil
	}
	return applied(), updateBucketConfig(ctx, tx, in)
}

func updateBucketConfig(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	op := in.Op
	_, err := tx.ExecContext(ctx,
		`UPDATE buckets SET policy = ?2, encryption_default = ?3, cache_max_age_seconds = ?4, config_msg_id = ?5 WHERE channel_id = ?1`,
		op.BucketChannelID, op.Policy, op.DefaultEncryption, op.CacheMaxAge, in.Observed.MsgID)
	if err != nil {
		return sqlFailure("update bucket config", err)
	}
	return nil
}
