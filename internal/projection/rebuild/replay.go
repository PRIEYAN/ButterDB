package rebuild

import (
	"context"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const opSavepoint = "rebuild_op"

type replayRow struct {
	msgID       int64
	opType      string
	rawHeader   string
	isDocument  bool
	actorUserID int64
	msgDate     int64
}

type replayScope struct {
	clause string
	args   []any
}

func (r *Rebuilder) replay(ctx context.Context, tx projection.Tx, channelID int64, scope replayScope) (Result, error) {
	var total Result
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("rebuild: %w", err)
		}
		batch, err := readReplayBatch(ctx, tx, channelID, after, scope)
		if err != nil {
			return Result{}, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		for _, row := range batch {
			rejected, err := r.applyRow(ctx, tx, channelID, row)
			if err != nil {
				return Result{}, err
			}
			if rejected {
				total.Rejected++
				continue
			}
			total.Applied++
		}
		after = batch[len(batch)-1].msgID
	}
}

func readReplayBatch(ctx context.Context, tx projection.Tx, channelID, after int64, scope replayScope) ([]replayRow, error) {
	query := `
		SELECT msg_id, op_type, raw_header, is_document, actor_user_id, msg_date
		FROM replay_log
		WHERE channel_id = ? AND msg_id > ? ` + scope.clause + `
		ORDER BY msg_id ASC
		LIMIT ?`
	args := append([]any{channelID, after}, scope.args...)
	args = append(args, rebuildReplayBatchSize)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("rebuild: scan replay_log of channel %d: %w", channelID, err)
	}
	defer rows.Close()
	batch := make([]replayRow, 0, rebuildReplayBatchSize)
	for rows.Next() {
		var row replayRow
		var isDocument int
		if err := rows.Scan(&row.msgID, &row.opType, &row.rawHeader, &isDocument, &row.actorUserID, &row.msgDate); err != nil {
			return nil, fmt.Errorf("rebuild: scan replay_log row of channel %d: %w", channelID, err)
		}
		row.isDocument = isDocument != 0
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rebuild: scan replay_log of channel %d: %w", channelID, err)
	}
	return batch, nil
}

func (r *Rebuilder) applyRow(ctx context.Context, tx projection.Tx, channelID int64, row replayRow) (bool, error) {
	op, err := wire.Parse(row.rawHeader)
	if errors.Is(err, wire.ErrWireUnknownOp) {
		return true, recordReject(ctx, tx, channelID, row, "", projection.RejectUnknownOp, wire.ErrWireUnknownOp.Error())
	}
	if err != nil {
		return false, fmt.Errorf("rebuild: parse msg %d of channel %d: %w", row.msgID, channelID, err)
	}
	applier, ok := r.registry.Lookup(op.Type)
	if !ok {
		return false, fmt.Errorf("rebuild: %w: applier for op type %q", errs.ErrMissingDependency, op.Type)
	}
	input := projection.ApplyInput{
		Observed: projection.Observed{
			ChannelID:   channelID,
			MsgID:       row.msgID,
			MsgDate:     row.msgDate,
			IsDocument:  row.isDocument,
			ActorUserID: row.actorUserID,
			RawText:     row.rawHeader,
		},
		Op: op,
	}
	return r.applyIsolated(ctx, tx, applier, input, row, op.JobID)
}

func (r *Rebuilder) applyIsolated(ctx context.Context, tx projection.Tx, applier projection.Applier, input projection.ApplyInput, row replayRow, jobID string) (bool, error) {
	channelID := input.Observed.ChannelID
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+opSavepoint); err != nil {
		return false, fmt.Errorf("rebuild: savepoint for msg %d of channel %d: %w", row.msgID, channelID, err)
	}
	result, applyErr := applier.Apply(ctx, tx, input)
	var reject *projection.Reject
	if errors.As(applyErr, &reject) {
		if err := settleRejectedSavepoint(ctx, tx, reject.Code); err != nil {
			return false, err
		}
		r.logger.Warn("rebuild: op rejected, continuing", "channel_id", channelID, "msg_id", row.msgID, "op_type", row.opType, "code", string(reject.Code))
		return true, recordReject(ctx, tx, channelID, row, jobID, reject.Code, reject.Detail)
	}
	if applyErr != nil {
		return false, fmt.Errorf("rebuild: apply msg %d of channel %d: %w", row.msgID, channelID, applyErr)
	}
	if err := releaseSavepoint(ctx, tx); err != nil {
		return false, err
	}
	if result.Kind != projection.OutcomeApplied && result.Kind != projection.OutcomeSuperseded {
		return false, fmt.Errorf("rebuild: apply msg %d of channel %d: %w: %q", row.msgID, channelID, ErrUnexpectedOutcome, string(result.Kind))
	}
	return false, recordAppliedOperation(ctx, tx, channelID, row, jobID, result.Kind)
}

func settleRejectedSavepoint(ctx context.Context, tx projection.Tx, code projection.RejectCode) error {
	// A duplicate op keeps the applier's writes, so its message stays queued for cleanup.
	if code == projection.RejectDuplicateOp {
		return releaseSavepoint(ctx, tx)
	}
	return rollbackSavepoint(ctx, tx)
}

func recordAppliedOperation(ctx context.Context, tx projection.Tx, channelID int64, row replayRow, jobID string, kind projection.OutcomeKind) error {
	if jobID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome, error)
		VALUES (?, ?, ?, ?, ?, '')
		ON CONFLICT (channel_id, op_id, op_type) DO NOTHING
	`, channelID, jobID, row.opType, row.msgID, string(kind))
	if err != nil {
		return fmt.Errorf("rebuild: record outcome of msg %d of channel %d: %w", row.msgID, channelID, err)
	}
	return nil
}

func rollbackSavepoint(ctx context.Context, tx projection.Tx) error {
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+opSavepoint); err != nil {
		return fmt.Errorf("rebuild: roll back savepoint: %w", err)
	}
	return releaseSavepoint(ctx, tx)
}

func releaseSavepoint(ctx context.Context, tx projection.Tx) error {
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+opSavepoint); err != nil {
		return fmt.Errorf("rebuild: release savepoint: %w", err)
	}
	return nil
}

func recordReject(ctx context.Context, tx projection.Tx, channelID int64, row replayRow, jobID string, code projection.RejectCode, detail string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO replay_log_rejects (channel_id, msg_id, code, op_type, job_id, raw_header, detail, msg_date)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, channelID, row.msgID, string(code), row.opType, jobID, row.rawHeader, detail, row.msgDate)
	if err != nil {
		return fmt.Errorf("rebuild: record reject of msg %d of channel %d: %w", row.msgID, channelID, err)
	}
	if jobID == "" || code == projection.RejectDuplicateOp {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome, error)
		VALUES (?, ?, ?, ?, 'rejected', ?)
	`, channelID, jobID, row.opType, row.msgID, string(code))
	if err != nil {
		return fmt.Errorf("rebuild: record outcome of msg %d of channel %d: %w", row.msgID, channelID, err)
	}
	return nil
}
