package difference

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	defaultPageSize     = 100
	maxFloodWaitRetries = 5
	maxFloodWaitSleep   = 60 * time.Second
)

var ErrNoProgress = errors.New("difference: channel difference made no progress")

type Dependencies struct {
	Gate        syncer.ChannelGate
	Channels    syncer.ChannelStore
	Cursors     syncer.SyncCursorStore
	Deletions   syncer.DeletionStore
	Scanner     syncer.HistoryScanner
	Ingester    syncer.OpIngester
	Differences DifferenceReader
	Writer      projection.Writer
	Sleeper     Sleeper
	Clock       clock.Clock
	Logger      *slog.Logger
}

type Service struct {
	deps Dependencies
}

type cursor struct {
	pts       int
	watermark int64
}

func New(deps Dependencies) (*Service, error) {
	missing := ""
	switch {
	case deps.Gate == nil:
		missing = "gate"
	case deps.Channels == nil:
		missing = "channels"
	case deps.Cursors == nil:
		missing = "cursors"
	case deps.Deletions == nil:
		missing = "deletions"
	case deps.Scanner == nil:
		missing = "scanner"
	case deps.Ingester == nil:
		missing = "ingester"
	case deps.Differences == nil:
		missing = "differences"
	case deps.Writer == nil:
		missing = "writer"
	case deps.Sleeper == nil:
		missing = "sleeper"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return nil, fmt.Errorf("difference: %w: %s", errs.ErrMissingDependency, missing)
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Sync(ctx context.Context, channelID int64) error {
	release, err := s.deps.Gate.Acquire(ctx, channelID)
	if err != nil {
		return fmt.Errorf("difference: acquire gate for channel %d: %w", channelID, err)
	}
	defer release()
	state, found, err := s.deps.Channels.Channel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("difference: load channel %d: %w", channelID, err)
	}
	if !found || state.Deleted {
		return fmt.Errorf("difference: channel %d: %w", channelID, syncer.ErrUnknownChannel)
	}
	if needsScan(state) {
		s.deps.Logger.Info("difference: channel needs a history scan before differences", "channel_id", channelID,
			"initial_sync_done", state.InitialSyncDone, "needs_projection_rebuild", state.NeedsProjectionRebuild, "pts", state.Pts)
		if err := s.deps.Scanner.EnsureAuthoritative(ctx, channelID); err != nil {
			return fmt.Errorf("difference: ensure authority for channel %d: %w", channelID, err)
		}
		return nil
	}
	return s.followDifference(ctx, state)
}

func needsScan(state syncer.ChannelState) bool {
	return !state.InitialSyncDone || state.NeedsProjectionRebuild || state.Pts <= 0
}

func (s *Service) followDifference(ctx context.Context, state syncer.ChannelState) error {
	channelID := state.ChannelID
	if state.AccessHash == 0 {
		return fmt.Errorf("difference: channel %d: %w", channelID, tgclient.ErrZeroAccessHash)
	}
	peer := tgclient.InputPeer{ChannelID: channelID, AccessHash: state.AccessHash}
	current := cursor{pts: state.Pts, watermark: state.LastSyncedMsg}
	for {
		diff, err := s.fetchDifference(ctx, peer, current.pts)
		if err != nil {
			return err
		}
		if diff.TooLong {
			s.deps.Logger.Info("difference: channel difference too long, falling back to history scan", "channel_id", channelID, "pts", current.pts)
			if err := s.deps.Scanner.FullScan(ctx, channelID); err != nil {
				return fmt.Errorf("difference: full scan of channel %d: %w", channelID, err)
			}
			return nil
		}
		next, err := s.commitPage(ctx, channelID, current, diff)
		if err != nil {
			return err
		}
		s.recordDeleted(ctx, channelID, diff.DeletedMessageIDs)
		if diff.Final {
			return nil
		}
		if next.pts <= current.pts {
			return fmt.Errorf("difference: channel %d at pts %d: %w", channelID, current.pts, ErrNoProgress)
		}
		current = next
	}
}

func (s *Service) fetchDifference(ctx context.Context, peer tgclient.InputPeer, pts int) (tgclient.ChannelDifference, error) {
	for retries := 0; ; retries++ {
		if err := ctx.Err(); err != nil {
			return tgclient.ChannelDifference{}, err
		}
		diff, err := s.deps.Differences.GetChannelDifference(ctx, peer, pts, defaultPageSize)
		if err == nil {
			return diff, nil
		}
		var flood *tgclient.FloodWaitError
		if !errors.As(err, &flood) || retries >= maxFloodWaitRetries {
			return tgclient.ChannelDifference{}, fmt.Errorf("difference: get channel difference for channel %d: %w", peer.ChannelID, err)
		}
		wait := min(flood.Duration, maxFloodWaitSleep)
		s.deps.Logger.Warn("difference: flood wait on channel difference, retrying", "channel_id", peer.ChannelID, "attempt", retries+1, "wait", wait)
		if err := s.deps.Sleeper.Sleep(ctx, wait); err != nil {
			return tgclient.ChannelDifference{}, err
		}
	}
}

func (s *Service) commitPage(ctx context.Context, channelID int64, current cursor, diff tgclient.ChannelDifference) (cursor, error) {
	messages := ascending(diff.Messages)
	next := cursor{pts: max(current.pts, diff.Pts), watermark: current.watermark}
	if len(messages) > 0 {
		next.watermark = max(next.watermark, messages[len(messages)-1].MsgID)
	}
	if len(messages) == 0 && next == current {
		return current, nil
	}
	s.deps.Logger.Debug("difference: applying channel difference page", "channel_id", channelID, "messages", len(messages), "deleted", len(diff.DeletedMessageIDs), "pts", next.pts)
	err := s.deps.Writer.Write(ctx, func(tx *sql.Tx) error {
		for _, message := range messages {
			if _, err := s.deps.Ingester.IngestTx(ctx, tx, observed(channelID, message)); err != nil {
				return err
			}
		}
		return s.deps.Cursors.SetSyncCursorTx(ctx, tx, channelID, next.watermark, next.pts)
	})
	if err != nil {
		return cursor{}, fmt.Errorf("difference: commit page for channel %d: %w", channelID, err)
	}
	return next, nil
}

func ascending(messages []tgclient.HistoryMessage) []tgclient.HistoryMessage {
	sorted := append([]tgclient.HistoryMessage(nil), messages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].MsgID < sorted[j].MsgID })
	return sorted
}

func observed(channelID int64, message tgclient.HistoryMessage) projection.Observed {
	return projection.Observed{
		ChannelID:   channelID,
		MsgID:       message.MsgID,
		MsgDate:     message.Date,
		IsDocument:  message.HasMedia,
		ActorUserID: message.FromID,
		RawText:     message.Text,
	}
}

func (s *Service) recordDeleted(ctx context.Context, channelID int64, msgIDs []int64) {
	if len(msgIDs) == 0 {
		return
	}
	err := s.writeDeleted(ctx, channelID, msgIDs)
	if err == nil {
		return
	}
	s.deps.Logger.Warn("difference: recording deleted messages as a batch failed, retrying one by one", "channel_id", channelID, "count", len(msgIDs), "error", err)
	for _, msgID := range msgIDs {
		if err := s.writeDeleted(ctx, channelID, []int64{msgID}); err != nil {
			s.deps.Logger.Error("difference: recording deleted message failed", "channel_id", channelID, "msg_id", msgID, "error", err)
		}
	}
}

func (s *Service) writeDeleted(ctx context.Context, channelID int64, msgIDs []int64) error {
	recordedAt := s.deps.Clock.Now().Unix()
	return s.deps.Writer.Write(ctx, func(tx *sql.Tx) error {
		removed, err := s.deps.Deletions.RecordDeletedTx(ctx, tx, channelID, msgIDs, syncer.DeletedExternal, recordedAt)
		if err != nil {
			return err
		}
		if len(removed) > 0 {
			s.deps.Logger.Info("difference: objects removed after their messages were deleted on Telegram", "channel_id", channelID, "objects", len(removed))
		}
		return nil
	})
}
