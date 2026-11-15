package remove

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const maxLastErrorBytes = 256

var _ ingest.CleanupDrainer = (*Drainer)(nil)

type DrainerDependencies struct {
	Queue    ingest.CleanupQueue
	Peers    PeerResolver
	Messages MessageDeleter
	Clock    clock.Clock
	Logger   *slog.Logger
}

type Drainer struct {
	queue    ingest.CleanupQueue
	peers    PeerResolver
	messages MessageDeleter
	clock    clock.Clock
	logger   *slog.Logger
}

func NewDrainer(deps DrainerDependencies) (*Drainer, error) {
	switch {
	case deps.Queue == nil:
		return nil, fmt.Errorf("remove: %w: cleanup queue", errs.ErrMissingDependency)
	case deps.Peers == nil:
		return nil, fmt.Errorf("remove: %w: peers", errs.ErrMissingDependency)
	case deps.Messages == nil:
		return nil, fmt.Errorf("remove: %w: message deleter", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("remove: %w: clock", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("remove: %w: logger", errs.ErrMissingDependency)
	}
	return &Drainer{queue: deps.Queue, peers: deps.Peers, messages: deps.Messages, clock: deps.Clock, logger: deps.Logger}, nil
}

func (d *Drainer) DrainChannel(ctx context.Context, channelID int64) error {
	peer, err := d.peers.PeerFor(ctx, channelID)
	if err != nil {
		return fmt.Errorf("remove: resolve channel %d for cleanup: %w", channelID, err)
	}
	var first error
	failed := map[int64]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return firstError(first, fmt.Errorf("remove: drain channel %d: %w", channelID, err))
		}
		now := d.clock.Now().Unix()
		ids, err := d.queue.ClaimCleanupBatch(ctx, channelID, now, limits.DeleteBatchSize)
		if err != nil {
			return firstError(first, fmt.Errorf("remove: claim cleanup batch for channel %d: %w", channelID, err))
		}
		if len(ids) == 0 {
			return first
		}
		if len(ids) > limits.DeleteBatchSize {
			ids = ids[:limits.DeleteBatchSize]
		}
		if ids = withoutFailed(failed, ids); len(ids) == 0 {
			return first
		}
		deleteErr := d.messages.DeleteMessages(ctx, peer, ids)
		if deleteErr == nil {
			if err := d.queue.ConfirmCleanupDeleted(ctx, channelID, ids, d.clock.Now().Unix()); err != nil {
				first = firstError(first, fmt.Errorf("remove: confirm cleanup for channel %d: %w", channelID, err))
			}
			continue
		}
		for _, id := range ids {
			failed[id] = struct{}{}
		}
		first = firstError(first, fmt.Errorf("remove: delete %d messages in channel %d: %w", len(ids), channelID, deleteErr))
		d.deferBatch(ctx, channelID, ids, deleteErr.Error(), &first)
	}
}

func (d *Drainer) deferBatch(ctx context.Context, channelID int64, ids []int64, reason string, first *error) {
	if err := d.queue.DeferCleanup(ctx, channelID, ids, d.clock.Now().Unix(), truncate(reason)); err != nil {
		*first = firstError(*first, fmt.Errorf("remove: defer cleanup for channel %d: %w", channelID, err))
		d.logger.Warn("remove: defer cleanup failed", "channel_id", channelID, "messages", len(ids))
	}
}

func withoutFailed(failed map[int64]struct{}, ids []int64) []int64 {
	kept := ids[:0:0]
	for _, id := range ids {
		if _, bad := failed[id]; !bad {
			kept = append(kept, id)
		}
	}
	return kept
}

func firstError(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

func truncate(text string) string {
	if len(text) <= maxLastErrorBytes {
		return text
	}
	return strings.ToValidUTF8(text[:maxLastErrorBytes], "")
}
