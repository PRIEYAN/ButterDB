package log

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const applySavepoint = "projection_log_apply"

type Dependencies struct {
	Registry projection.Registry
	Logger   *slog.Logger
}

type Projector struct {
	registry projection.Registry
	logger   *slog.Logger
}

func NewProjector(deps Dependencies) (*Projector, error) {
	if deps.Registry == nil {
		return nil, fmt.Errorf("projection log: %w: registry", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("projection log: %w: logger", errs.ErrMissingDependency)
	}
	return &Projector{registry: deps.Registry, logger: deps.Logger}, nil
}

func (p *Projector) ProjectFromOp(ctx context.Context, w projection.Writer, obs projection.Observed) (projection.Outcome, error) {
	if w == nil {
		return projection.Outcome{}, fmt.Errorf("projection log: %w: writer", errs.ErrMissingDependency)
	}
	var outcome projection.Outcome
	err := w.Write(ctx, func(tx *sql.Tx) error {
		var projectErr error
		outcome, projectErr = p.ProjectFromOpTx(ctx, tx, obs)
		return projectErr
	})
	if err != nil {
		return projection.Outcome{}, err
	}
	return outcome, nil
}

func (p *Projector) ProjectFromOpTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error) {
	if tx == nil {
		return projection.Outcome{}, fmt.Errorf("projection log: %w: transaction", errs.ErrMissingDependency)
	}
	if obs.ChannelID <= 0 || obs.MsgID <= 0 {
		return projection.Outcome{}, fmt.Errorf("projection log: channel id and msg id are required")
	}
	if !wire.HasPrefix(obs.RawText) {
		return projection.Outcome{Kind: projection.OutcomeIgnored}, nil
	}
	header := headerOf(obs.RawText)
	hash := HashHeader(header)
	seen, found, err := readRecorded(ctx, tx, obs.ChannelID, obs.MsgID)
	if err != nil {
		return projection.Outcome{}, err
	}
	if found {
		return p.observeAgain(ctx, tx, obs, seen, hash, header)
	}
	op, parseErr := wire.Parse(header)
	if parseErr != nil && !errors.Is(parseErr, wire.ErrWireUnknownOp) {
		return p.recordMalformed(ctx, tx, obs, header, parseErr)
	}
	row := logRow{obs: obs, op: op, header: header, hash: hash}
	if err := insertLogRow(ctx, tx, row); err != nil {
		return projection.Outcome{}, err
	}
	if parseErr != nil {
		return p.recordUnknownOp(ctx, tx, row)
	}
	return p.apply(ctx, tx, row)
}

func (p *Projector) observeAgain(ctx context.Context, tx projection.Tx, obs projection.Observed, seen recorded, hash, header string) (projection.Outcome, error) {
	outcome := projection.Outcome{Kind: projection.OutcomeAlreadySeen, OpType: wire.OpType(seen.opType), Key: seen.keyText.String}
	if seen.hash == hash {
		return outcome, nil
	}
	if err := recordTamper(ctx, tx, obs, seen.hash, hash, header); err != nil {
		return projection.Outcome{}, err
	}
	p.logger.Warn("projection log: header tamper detected, keeping the first op canonical",
		"channel_id", obs.ChannelID, "msg_id", obs.MsgID)
	outcome.Kind = projection.OutcomeTampered
	return outcome, nil
}

func (p *Projector) recordMalformed(ctx context.Context, tx projection.Tx, obs projection.Observed, header string, parseErr error) (projection.Outcome, error) {
	reject := &projection.Reject{Code: projection.RejectMalformed, Detail: parseErr.Error()}
	if err := recordReject(ctx, tx, obs, header, wire.Op{}, reject); err != nil {
		return projection.Outcome{}, err
	}
	p.logRejected(obs, reject.Code, "")
	return projection.Outcome{Kind: projection.OutcomeRejected, RejectCode: reject.Code}, nil
}

func (p *Projector) recordUnknownOp(ctx context.Context, tx projection.Tx, row logRow) (projection.Outcome, error) {
	reject := &projection.Reject{Code: projection.RejectUnknownOp, Detail: wire.ErrWireUnknownOp.Error()}
	if err := recordReject(ctx, tx, row.obs, row.header, row.op, reject); err != nil {
		return projection.Outcome{}, err
	}
	p.logRejected(row.obs, reject.Code, row.op.Type)
	return projection.Outcome{Kind: projection.OutcomeRejected, OpType: row.op.Type, RejectCode: reject.Code}, nil
}

func (p *Projector) apply(ctx context.Context, tx projection.Tx, row logRow) (projection.Outcome, error) {
	applier, ok := p.registry.Lookup(row.op.Type)
	if !ok || applier == nil {
		return projection.Outcome{}, fmt.Errorf("projection log: no applier registered for op type %q", row.op.Type)
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+applySavepoint); err != nil {
		return projection.Outcome{}, fmt.Errorf("projection log: open savepoint: %w", err)
	}
	result, applyErr := applier.Apply(ctx, tx, projection.ApplyInput{Observed: row.obs, Op: row.op})
	var reject *projection.Reject
	if errors.As(applyErr, &reject) {
		return p.recordApplyReject(ctx, tx, row, reject)
	}
	if applyErr != nil {
		return projection.Outcome{}, fmt.Errorf("projection log: apply %s msg=%d: %w", row.op.Type, row.obs.MsgID, applyErr)
	}
	if _, err := tx.ExecContext(ctx, "RELEASE "+applySavepoint); err != nil {
		return projection.Outcome{}, fmt.Errorf("projection log: release savepoint: %w", err)
	}
	return p.recordApplied(ctx, tx, row, result)
}

func (p *Projector) recordApplied(ctx context.Context, tx projection.Tx, row logRow, result projection.ApplyResult) (projection.Outcome, error) {
	if result.Kind != projection.OutcomeApplied && result.Kind != projection.OutcomeSuperseded {
		return projection.Outcome{}, fmt.Errorf("projection log: applier for %s returned outcome %q", row.op.Type, result.Kind)
	}
	if err := recordOperationOutcome(ctx, tx, row.obs, row.op, result.Kind, ""); err != nil {
		return projection.Outcome{}, err
	}
	key, _ := wire.KeyOf(row.op)
	return projection.Outcome{Kind: result.Kind, OpType: row.op.Type, Key: key}, nil
}

func (p *Projector) recordApplyReject(ctx context.Context, tx projection.Tx, row logRow, reject *projection.Reject) (projection.Outcome, error) {
	if reject.Code != projection.RejectDuplicateOp {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO "+applySavepoint); err != nil {
			return projection.Outcome{}, fmt.Errorf("projection log: roll back savepoint: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE "+applySavepoint); err != nil {
		return projection.Outcome{}, fmt.Errorf("projection log: release savepoint: %w", err)
	}
	if err := recordReject(ctx, tx, row.obs, row.header, row.op, reject); err != nil {
		return projection.Outcome{}, err
	}
	if reject.Code != projection.RejectDuplicateOp {
		if err := recordOperationOutcome(ctx, tx, row.obs, row.op, projection.OutcomeRejected, string(reject.Code)); err != nil {
			return projection.Outcome{}, err
		}
	}
	p.logRejected(row.obs, reject.Code, row.op.Type)
	key, _ := wire.KeyOf(row.op)
	return projection.Outcome{Kind: projection.OutcomeRejected, OpType: row.op.Type, Key: key, RejectCode: reject.Code}, nil
}

func (p *Projector) logRejected(obs projection.Observed, code projection.RejectCode, opType wire.OpType) {
	p.logger.Warn("projection log: op rejected, replay continues",
		"channel_id", obs.ChannelID, "msg_id", obs.MsgID, "code", string(code), "op_type", string(opType))
}
