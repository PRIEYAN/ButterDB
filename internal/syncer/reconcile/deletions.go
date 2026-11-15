package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const MinInterval = 24 * time.Hour

type MessageChecker interface {
	GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error)
}

type Dependencies struct {
	Gate      syncer.ChannelGate
	Channels  syncer.ChannelStore
	Live      syncer.LiveObjects
	Deletions syncer.DeletionStore
	Cursors   syncer.SyncCursorStore
	Telegram  MessageChecker
	Writer    projection.Writer
	Clock     clock.Clock
	Logger    *slog.Logger
}

type Reconciler struct {
	deps Dependencies
}

type Result struct {
	Skipped       bool
	Checked       int
	MissingMsgIDs []int64
	RemovedKeys   []string
}

func NewReconciler(deps Dependencies) (*Reconciler, error) {
	switch {
	case deps.Gate == nil:
		return nil, fmt.Errorf("reconcile: %w: gate", errs.ErrMissingDependency)
	case deps.Channels == nil:
		return nil, fmt.Errorf("reconcile: %w: channels", errs.ErrMissingDependency)
	case deps.Live == nil:
		return nil, fmt.Errorf("reconcile: %w: live objects", errs.ErrMissingDependency)
	case deps.Deletions == nil:
		return nil, fmt.Errorf("reconcile: %w: deletions", errs.ErrMissingDependency)
	case deps.Cursors == nil:
		return nil, fmt.Errorf("reconcile: %w: cursors", errs.ErrMissingDependency)
	case deps.Telegram == nil:
		return nil, fmt.Errorf("reconcile: %w: telegram", errs.ErrMissingDependency)
	case deps.Writer == nil:
		return nil, fmt.Errorf("reconcile: %w: writer", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("reconcile: %w: clock", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("reconcile: %w: logger", errs.ErrMissingDependency)
	}
	return &Reconciler{deps: deps}, nil
}

func (r *Reconciler) ReconcileDeletions(ctx context.Context, channelID int64) (Result, error) {
	release, err := r.deps.Gate.Acquire(ctx, channelID)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: acquire channel gate: %w", err)
	}
	defer release()

	state, ok, err := r.deps.Channels.Channel(ctx, channelID)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: load channel: %w", err)
	}
	if !ok {
		return Result{}, fmt.Errorf("reconcile: channel %d: %w", channelID, syncer.ErrUnknownChannel)
	}
	now := r.deps.Clock.Now()
	if state.Deleted || (state.LastReconciledAt > 0 && now.Sub(time.UnixMilli(state.LastReconciledAt)) < MinInterval) {
		return Result{Skipped: true}, nil
	}

	refs, err := r.deps.Live.LiveMessageIDs(ctx, channelID)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: list live objects: %w", err)
	}
	ids := backingIDs(refs)
	if len(ids) == 0 {
		return Result{}, nil
	}
	if state.AccessHash == 0 {
		return Result{}, fmt.Errorf("reconcile: channel %d: %w", channelID, tgclient.ErrZeroAccessHash)
	}

	missing, err := r.missing(ctx, tgclient.InputPeer{ChannelID: channelID, AccessHash: state.AccessHash}, ids)
	if err != nil {
		return Result{}, err
	}
	result := Result{Checked: len(ids), MissingMsgIDs: missing}
	if len(missing) > 0 {
		removed, err := r.record(ctx, channelID, missing, now.UnixMilli())
		if err != nil {
			return Result{}, err
		}
		result.RemovedKeys = removed
		r.deps.Logger.Warn("reconcile: backing messages deleted on telegram", "channel_id", channelID, "missing", len(missing), "removed_objects", len(removed))
	}
	if err := r.deps.Cursors.SetLastReconciledAt(ctx, channelID, now.UnixMilli()); err != nil {
		return Result{}, fmt.Errorf("reconcile: store last reconciled time: %w", err)
	}
	return result, nil
}

func backingIDs(refs []syncer.LiveObjectRef) []int64 {
	var ids []int64
	for _, ref := range refs {
		ids = append(ids, ref.BackingMsgIDs...)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func (r *Reconciler) missing(ctx context.Context, peer tgclient.InputPeer, ids []int64) ([]int64, error) {
	var missing []int64
	for start := 0; start < len(ids); start += limits.DeleteBatchSize {
		batch := ids[start:min(start+limits.DeleteBatchSize, len(ids))]
		answer, err := r.deps.Telegram.GetMessages(ctx, peer, batch)
		if err != nil {
			return nil, fmt.Errorf("reconcile: check message existence: %w", err)
		}
		present := make(map[int64]struct{}, len(answer))
		for _, m := range answer {
			if !m.Placeholder {
				present[m.MsgID] = struct{}{}
			}
		}
		for _, id := range batch {
			if _, ok := present[id]; !ok {
				missing = append(missing, id)
			}
		}
	}
	return missing, nil
}

func (r *Reconciler) record(ctx context.Context, channelID int64, missing []int64, at int64) ([]string, error) {
	var removed []string
	err := r.deps.Writer.Write(ctx, func(tx *sql.Tx) error {
		keys, err := r.deps.Deletions.RecordDeletedTx(ctx, tx, channelID, missing, syncer.DeletedReconcile, at)
		if err != nil {
			return err
		}
		removed = keys
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: record deleted messages: %w", err)
	}
	return removed, nil
}
