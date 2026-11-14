package apply

import (
	"context"
	"database/sql"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type bucketDeleteApplier struct {
	generation KeyGeneration
}

func (a bucketDeleteApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if replayed, err := isReplayedDelete(ctx, tx, in); err != nil || replayed {
		return applied(), err
	}
	if _, err := requireNamedBucket(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := requireFirstOperation(ctx, tx, in.Observed.ChannelID, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := retireBucket(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	a.generation.Advance(in.Op.BucketChannelID)
	return applied(), nil
}

func isReplayedDelete(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (bool, error) {
	var deletedMsgID int64
	err := tx.QueryRowContext(ctx,
		`SELECT deleted_msg_id FROM buckets WHERE channel_id = ?1`, in.Op.BucketChannelID).Scan(&deletedMsgID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sqlFailure("load bucket deletion", err)
	}
	return deletedMsgID == in.Observed.MsgID, nil
}

func retireBucket(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	channelID := in.Op.BucketChannelID
	statements := []string{
		`DELETE FROM objects WHERE channel_id = ?1`,
		`DELETE FROM object_tombstones WHERE channel_id = ?1`,
		`DELETE FROM object_bodies WHERE channel_id = ?1`,
		`DELETE FROM pending_message_cleanup WHERE channel_id = ?1`,
		`DELETE FROM encryption_keys WHERE bucket_channel_id = ?1`,
		`UPDATE channels SET state = 'deleted' WHERE channel_id = ?1`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, channelID); err != nil {
			return sqlFailure("remove bucket rows", err)
		}
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE buckets SET deleted_msg_id = ?2, object_count = 0, stored_bytes = 0, plaintext_bytes = 0 WHERE channel_id = ?1`,
		channelID, in.Observed.MsgID)
	if err != nil {
		return sqlFailure("mark bucket deleted", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO pending_channel_cleanup (channel_id, bdel_msg_id) VALUES (?1, ?2)`, channelID, in.Observed.MsgID)
	if err != nil {
		return sqlFailure("queue channel cleanup", err)
	}
	return nil
}
