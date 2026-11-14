package difference

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

var _ syncer.OpIngester = (*Ingester)(nil)

type IngesterDependencies struct {
	Projector OpProjector
	Repairs   KeyRepairer
	Logger    *slog.Logger
}

type Ingester struct {
	deps IngesterDependencies
}

func NewIngester(deps IngesterDependencies) (*Ingester, error) {
	if deps.Projector == nil {
		return nil, fmt.Errorf("difference: %w: projector", errs.ErrMissingDependency)
	}
	if deps.Repairs == nil {
		return nil, fmt.Errorf("difference: %w: key repairs", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("difference: %w: logger", errs.ErrMissingDependency)
	}
	return &Ingester{deps: deps}, nil
}

func (i *Ingester) IngestTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error) {
	outcome, err := i.deps.Projector.ProjectFromOpTx(ctx, tx, obs)
	if err != nil {
		return projection.Outcome{}, fmt.Errorf("difference: project msg %d in channel %d: %w", obs.MsgID, obs.ChannelID, err)
	}
	if !recordsNewKeyOp(outcome) {
		return outcome, nil
	}
	needed, err := i.deps.Repairs.NeedsKeyRepair(ctx, tx, obs.ChannelID, outcome.Key, obs.MsgID)
	if err != nil {
		return projection.Outcome{}, fmt.Errorf("difference: check key order for msg %d in channel %d: %w", obs.MsgID, obs.ChannelID, err)
	}
	if !needed {
		return outcome, nil
	}
	if _, err := i.deps.Repairs.RepairKeyTx(ctx, tx, obs.ChannelID, outcome.Key); err != nil {
		return projection.Outcome{}, fmt.Errorf("difference: repair key after msg %d in channel %d: %w", obs.MsgID, obs.ChannelID, err)
	}
	i.deps.Logger.Info("difference: out-of-order op repaired its key alone", "channel_id", obs.ChannelID, "msg_id", obs.MsgID, "op_type", string(outcome.OpType))
	return outcome, nil
}

func recordsNewKeyOp(outcome projection.Outcome) bool {
	if outcome.Key == "" || (outcome.OpType != wire.OpPut && outcome.OpType != wire.OpDel) {
		return false
	}
	switch outcome.Kind {
	case projection.OutcomeApplied, projection.OutcomeSuperseded:
		return true
	case projection.OutcomeRejected:
		return outcome.RejectCode == projection.RejectDuplicateOp
	}
	return false
}
