package apply

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type putApplier struct{}

func (putApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if _, err := requireBucketScope(ctx, tx, in, false); err != nil {
		return projection.ApplyResult{}, err
	}
	if err := checkPutShape(in); err != nil {
		return projection.ApplyResult{}, err
	}
	channelID := in.Observed.ChannelID
	duplicate, err := isDuplicateOperation(ctx, tx, channelID, in)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if duplicate {
		return projection.ApplyResult{}, rejectDuplicatePut(ctx, tx, in)
	}
	return resolvePut(ctx, tx, in)
}

func checkPutShape(in projection.ApplyInput) error {
	op := in.Op
	if op.BodyMsgID >= in.Observed.MsgID {
		return rejectf(projection.RejectBadBodyRef, "body message %d is not below put message %d", op.BodyMsgID, in.Observed.MsgID)
	}
	if op.StoredSize > limits.MaxStoredObjectBytes {
		return rejectf(projection.RejectSizeMismatch, "stored size %d exceeds the ceiling", op.StoredSize)
	}
	if op.Encrypted && op.StoredSize != format.CiphertextSize(op.PlaintextSize) {
		return rejectf(projection.RejectSizeMismatch, "stored size %d does not match plaintext size %d", op.StoredSize, op.PlaintextSize)
	}
	return nil
}

func rejectDuplicatePut(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	obs := in.Observed
	if err := queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, cleanupDuplicate, obs.MsgID); err != nil {
		return err
	}
	return duplicateOperationReject(in)
}

func resolvePut(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	obs := in.Observed
	current, hasCurrent, err := loadObject(ctx, tx, obs.ChannelID, in.Op.Key)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if hasCurrent && current.PutMsgID == obs.MsgID {
		return applied(), nil
	}
	gone, err := putIsGone(ctx, tx, in)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if gone {
		return applyGonePut(ctx, tx, in, current, hasCurrent)
	}
	tombstone, err := loadTombstoneMsgID(ctx, tx, obs.ChannelID, in.Op.Key)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if tombstone > obs.MsgID {
		return supersededPut(ctx, tx, in, cleanupBlocked)
	}
	if hasCurrent && current.PutMsgID > obs.MsgID {
		return supersededPut(ctx, tx, in, cleanupSuperseded)
	}
	return replacePut(ctx, tx, in, current, hasCurrent)
}

func putIsGone(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (bool, error) {
	putGone, err := isMessageDeleted(ctx, tx, in.Observed.ChannelID, in.Observed.MsgID)
	if err != nil || putGone {
		return putGone, err
	}
	return isMessageDeleted(ctx, tx, in.Observed.ChannelID, in.Op.BodyMsgID)
}

func supersededPut(ctx context.Context, tx projection.Tx, in projection.ApplyInput, reason string) (projection.ApplyResult, error) {
	obs := in.Observed
	if err := queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, reason, obs.MsgID, in.Op.BodyMsgID); err != nil {
		return projection.ApplyResult{}, err
	}
	return superseded(), nil
}

func applyGonePut(ctx context.Context, tx projection.Tx, in projection.ApplyInput, current objectRow, hasCurrent bool) (projection.ApplyResult, error) {
	obs := in.Observed
	if hasCurrent && current.PutMsgID < obs.MsgID {
		if err := removeObject(ctx, tx, obs.ChannelID, in.Op.Key, current); err != nil {
			return projection.ApplyResult{}, err
		}
		err := queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, cleanupSuperseded, current.PutMsgID, current.BodyMsgID)
		if err != nil {
			return projection.ApplyResult{}, err
		}
	}
	if err := queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, cleanupSuperseded, obs.MsgID, in.Op.BodyMsgID); err != nil {
		return projection.ApplyResult{}, err
	}
	return applied(), nil
}

func replacePut(ctx context.Context, tx projection.Tx, in projection.ApplyInput, current objectRow, hasCurrent bool) (projection.ApplyResult, error) {
	obs := in.Observed
	if hasCurrent {
		if err := removeObject(ctx, tx, obs.ChannelID, in.Op.Key, current); err != nil {
			return projection.ApplyResult{}, err
		}
	}
	if err := insertObject(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	if hasCurrent {
		err := queueCleanup(ctx, tx, obs.ChannelID, in.Op.Key, obs.MsgID, cleanupSuperseded, current.PutMsgID, current.BodyMsgID)
		if err != nil {
			return projection.ApplyResult{}, err
		}
	}
	return applied(), nil
}
