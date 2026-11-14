package scan

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const spoolBatchSize = 256

func (s *Scanner) FullScan(ctx context.Context, channelID int64) error {
	started := s.clock.Now()
	state, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return err
	}
	peer, err := s.resolvePeer(ctx, channelID)
	if err != nil {
		return err
	}
	pts, err := s.samplePts(ctx, channelID, peer)
	if err != nil {
		return err
	}
	if err := s.spool.ClearSpool(ctx, channelID); err != nil {
		return fmt.Errorf("scan: clear spool for channel %d: %w", channelID, err)
	}
	plan, err := s.planHistory(ctx, channelID, peer, 0)
	if err != nil {
		return err
	}
	if err := s.spoolHistoryPlan(ctx, channelID, peer, plan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = s.writer.Write(ctx, func(tx *sql.Tx) error {
		return s.applySpool(ctx, tx, state, plan.highestSeen, newestPts(pts, state.Pts))
	})
	if err != nil {
		return fmt.Errorf("scan: apply full scan of channel %d: %w", channelID, err)
	}
	if err := s.spool.ClearSpool(ctx, channelID); err != nil {
		s.logger.Warn("scan: could not clear the spool", "channel_id", channelID, "error", err)
	}
	s.logger.Info("scan: full scan completed", "channel_id", channelID, "pages", len(plan.upperBounds),
		"highest_msg_id", plan.highestSeen, "elapsed", s.clock.Now().Sub(started))
	return nil
}

func (s *Scanner) spoolHistoryPlan(ctx context.Context, channelID int64, peer tgclient.InputPeer, plan historyPlan) error {
	for i := len(plan.upperBounds) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := s.getHistory(ctx, channelID, peer, plan.upperBounds[i])
		if err != nil {
			return fmt.Errorf("scan: get history: %w", err)
		}
		kept, _ := filterHistoryPage(page, 0, plan.highestSeen)
		if err := s.spoolKept(ctx, channelID, kept); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) spoolKept(ctx context.Context, channelID int64, kept []tgclient.HistoryMessage) error {
	for start := 0; start < len(kept); start += spoolBatchSize {
		end := min(start+spoolBatchSize, len(kept))
		if err := s.spool.SpoolPage(ctx, channelID, kept[start:end]); err != nil {
			return fmt.Errorf("scan: spool page for channel %d: %w", channelID, err)
		}
	}
	return nil
}

func (s *Scanner) applySpool(ctx context.Context, tx *sql.Tx, state syncer.ChannelState, highestSeen int64, pts int) error {
	if err := s.ingestSpool(ctx, tx, state.ChannelID); err != nil {
		return err
	}
	if state.NeedsProjectionRebuild {
		if err := s.rebuildAfterScan(ctx, tx, state); err != nil {
			return err
		}
	}
	return s.cursors.MarkInitialSyncDoneTx(ctx, tx, state.ChannelID, highestSeen, pts)
}

func (s *Scanner) ingestSpool(ctx context.Context, tx *sql.Tx, channelID int64) error {
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := s.spool.SpooledPageTx(ctx, tx, channelID, after, spoolBatchSize)
		if err != nil {
			return fmt.Errorf("scan: read spool of channel %d: %w", channelID, err)
		}
		if len(batch) == 0 {
			return nil
		}
		if err := s.ingestMessages(ctx, tx, channelID, batch); err != nil {
			return err
		}
		after = batch[len(batch)-1].MsgID
	}
}

func (s *Scanner) ingestMessages(ctx context.Context, tx *sql.Tx, channelID int64, msgs []tgclient.HistoryMessage) error {
	parsed := ParseHistoryPage(channelID, msgs)
	SortAscending(parsed)
	for _, message := range parsed {
		if _, err := s.ingester.IngestTx(ctx, tx, message.Observed); err != nil {
			return fmt.Errorf("scan: ingest msg %d of channel %d: %w", message.MsgID, channelID, err)
		}
	}
	return nil
}

// Ops banked while the objects they target were unread were skipped again above, so only replaying the completed log lands them.
func (s *Scanner) rebuildAfterScan(ctx context.Context, tx *sql.Tx, state syncer.ChannelState) error {
	var result rebuild.Result
	var err error
	if state.Kind == syncer.KindRegistry {
		result, err = s.rebuilder.RebuildAllTx(ctx, tx)
	} else {
		result, err = s.rebuilder.RebuildChannelTx(ctx, tx, state.ChannelID)
	}
	if err != nil {
		return fmt.Errorf("scan: rebuild after full scan of channel %d: %w", state.ChannelID, err)
	}
	s.logger.Info("scan: rebuilt projection from the completed log", "channel_id", state.ChannelID,
		"applied", result.Applied, "rejected", result.Rejected)
	return nil
}
