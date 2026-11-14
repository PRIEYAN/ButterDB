package apply

import (
	"context"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type bucketCreateApplier struct{}

func (bucketCreateApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if _, err := bucket.ParseName(in.Op.Bucket); err != nil {
		if errors.Is(err, bucket.ErrInvalidBucketName) {
			return projection.ApplyResult{}, rejectf(projection.RejectBucketNameInvalid, "bucket name %q is not valid", in.Op.Bucket)
		}
		return projection.ApplyResult{}, sqlFailure("validate bucket name", err)
	}
	if err := requireFirstOperation(ctx, tx, in.Observed.ChannelID, in); err != nil {
		return projection.ApplyResult{}, err
	}
	existing, found, err := loadBucket(ctx, tx, in.Op.BucketChannelID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if found {
		return replayedCreate(existing, in)
	}
	if err := requireFreeName(ctx, tx, in.Op.Bucket); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := ensureBucketChannel(ctx, tx, in.Op.BucketChannelID); err != nil {
		return projection.ApplyResult{}, err
	}
	return applied(), insertBucket(ctx, tx, in)
}

func replayedCreate(existing bucketRow, in projection.ApplyInput) (projection.ApplyResult, error) {
	if existing.CreatedMsgID == in.Observed.MsgID {
		return applied(), nil
	}
	return projection.ApplyResult{}, rejectf(projection.RejectBucketExists, "channel %d already belongs to a bucket", in.Op.BucketChannelID)
}

func requireFreeName(ctx context.Context, tx projection.Tx, name string) error {
	var live int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM buckets WHERE name = ?1 AND deleted_msg_id = 0`, name).Scan(&live)
	if err != nil {
		return sqlFailure("count live buckets by name", err)
	}
	if live > 0 {
		return rejectf(projection.RejectBucketExists, "a live bucket already has the name %q", name)
	}
	return nil
}

func ensureBucketChannel(ctx context.Context, tx projection.Tx, channelID int64) error {
	kind, err := channelKind(ctx, tx, channelID)
	if err != nil {
		return err
	}
	if kind == channelKindRegistry {
		return rejectf(projection.RejectBucketExists, "channel %d is the registry channel", channelID)
	}
	if kind == channelKindBucket {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO channels (channel_id, kind, access_hash, title, state) VALUES (?1, 'bucket', 0, '', 'active')`, channelID)
	if err != nil {
		return sqlFailure("insert bucket channel", err)
	}
	return nil
}

func insertBucket(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	op := in.Op
	_, err := tx.ExecContext(ctx,
		`INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?6)`,
		op.BucketChannelID, op.Bucket, op.Policy, op.DefaultEncryption, op.CacheMaxAge, in.Observed.MsgID, op.JobID, in.Observed.MsgDate)
	if err != nil {
		return sqlFailure("insert bucket", err)
	}
	return nil
}
