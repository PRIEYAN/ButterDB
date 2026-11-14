package apply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

const (
	channelKindRegistry = "registry"
	channelKindBucket   = "bucket"
)

type bucketRow struct {
	ChannelID    int64
	Name         string
	ConfigMsgID  int64
	CreatedMsgID int64
	DeletedMsgID int64
}

func rejectf(code projection.RejectCode, format string, args ...any) error {
	return &projection.Reject{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func sqlFailure(what string, err error) error {
	return fmt.Errorf("apply: %s: %w", what, err)
}

func channelKind(ctx context.Context, tx projection.Tx, channelID int64) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM channels WHERE channel_id = ?1`, channelID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", sqlFailure("load channel kind", err)
	}
	return kind, nil
}

func requireRegistryScope(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	if in.Observed.IsDocument {
		return rejectf(projection.RejectWrongCarrier, "%s must be a text message", in.Op.Type)
	}
	kind, err := channelKind(ctx, tx, in.Observed.ChannelID)
	if err != nil {
		return err
	}
	if kind != channelKindRegistry {
		return rejectf(projection.RejectWrongChannel, "%s belongs in the registry channel", in.Op.Type)
	}
	return nil
}

func requireBucketScope(ctx context.Context, tx projection.Tx, in projection.ApplyInput, onDocument bool) (bucketRow, error) {
	if in.Observed.IsDocument != onDocument {
		return bucketRow{}, rejectf(projection.RejectWrongCarrier, "%s is on the wrong message carrier", in.Op.Type)
	}
	kind, err := channelKind(ctx, tx, in.Observed.ChannelID)
	if err != nil {
		return bucketRow{}, err
	}
	if kind == channelKindRegistry {
		return bucketRow{}, rejectf(projection.RejectWrongChannel, "%s belongs in a bucket channel", in.Op.Type)
	}
	row, found, err := loadBucket(ctx, tx, in.Observed.ChannelID)
	if err != nil {
		return bucketRow{}, err
	}
	if !found || row.DeletedMsgID != 0 {
		return bucketRow{}, rejectf(projection.RejectBucketNotFound, "no live bucket for channel %d", in.Observed.ChannelID)
	}
	return row, nil
}

func loadBucket(ctx context.Context, tx projection.Tx, channelID int64) (bucketRow, bool, error) {
	var row bucketRow
	err := tx.QueryRowContext(ctx,
		`SELECT channel_id, name, config_msg_id, created_msg_id, deleted_msg_id FROM buckets WHERE channel_id = ?1`,
		channelID).Scan(&row.ChannelID, &row.Name, &row.ConfigMsgID, &row.CreatedMsgID, &row.DeletedMsgID)
	if errors.Is(err, sql.ErrNoRows) {
		return bucketRow{}, false, nil
	}
	if err != nil {
		return bucketRow{}, false, sqlFailure("load bucket", err)
	}
	return row, true, nil
}

func requireNamedBucket(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (bucketRow, error) {
	row, found, err := loadBucket(ctx, tx, in.Op.BucketChannelID)
	if err != nil {
		return bucketRow{}, err
	}
	if !found || row.DeletedMsgID != 0 || row.Name != in.Op.Bucket {
		return bucketRow{}, rejectf(projection.RejectBucketNotFound, "no live bucket %q on channel %d", in.Op.Bucket, in.Op.BucketChannelID)
	}
	return row, nil
}

func isDuplicateOperation(ctx context.Context, tx projection.Tx, channelID int64, in projection.ApplyInput) (bool, error) {
	var firstMsgID int64
	err := tx.QueryRowContext(ctx,
		`SELECT msg_id FROM projection_operations WHERE channel_id = ?1 AND op_id = ?2 AND op_type = ?3`,
		channelID, in.Op.JobID, string(in.Op.Type)).Scan(&firstMsgID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sqlFailure("load projection operation", err)
	}
	return firstMsgID != in.Observed.MsgID, nil
}

func duplicateOperationReject(in projection.ApplyInput) error {
	return rejectf(projection.RejectDuplicateOp, "job already projected for %s at another message", in.Op.Type)
}

func requireFirstOperation(ctx context.Context, tx projection.Tx, channelID int64, in projection.ApplyInput) error {
	duplicate, err := isDuplicateOperation(ctx, tx, channelID, in)
	if err != nil {
		return err
	}
	if duplicate {
		return duplicateOperationReject(in)
	}
	return nil
}

func applied() projection.ApplyResult {
	return projection.ApplyResult{Kind: projection.OutcomeApplied}
}

func superseded() projection.ApplyResult {
	return projection.ApplyResult{Kind: projection.OutcomeSuperseded}
}
