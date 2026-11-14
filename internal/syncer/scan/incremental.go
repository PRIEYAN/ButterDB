package scan

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (s *Scanner) Incremental(ctx context.Context, channelID int64) error {
	state, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if state.NeedsProjectionRebuild || !state.InitialSyncDone {
		s.logger.Info("scan: no authority or rebuild pending, escalating to a full scan", "channel_id", channelID)
		return s.FullScan(ctx, channelID)
	}
	peer, err := s.resolvePeer(ctx, channelID)
	if err != nil {
		return err
	}
	pts, err := s.samplePts(ctx, channelID, peer)
	if err != nil {
		return err
	}
	plan, err := s.planHistory(ctx, channelID, peer, state.LastSyncedMsg)
	if err != nil {
		return err
	}
	watermark, err := s.applyHistoryPlan(ctx, state, peer, plan)
	if err != nil {
		return err
	}
	return s.storeSampledPts(ctx, state, watermark, pts)
}

func (s *Scanner) applyHistoryPlan(ctx context.Context, state syncer.ChannelState, peer tgclient.InputPeer, plan historyPlan) (int64, error) {
	watermark := state.LastSyncedMsg
	for i := len(plan.upperBounds) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return watermark, err
		}
		page, err := s.getHistory(ctx, state.ChannelID, peer, plan.upperBounds[i])
		if err != nil {
			return watermark, fmt.Errorf("scan: get history: %w", err)
		}
		kept, pageWatermark := filterHistoryPage(page, watermark, plan.highestSeen)
		if err := s.commitPage(ctx, state, kept, pageWatermark); err != nil {
			return watermark, err
		}
		watermark = pageWatermark
	}
	return watermark, nil
}

func (s *Scanner) commitPage(ctx context.Context, state syncer.ChannelState, kept []tgclient.HistoryMessage, watermark int64) error {
	if len(kept) == 0 {
		return nil
	}
	err := s.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := s.ingestMessages(ctx, tx, state.ChannelID, kept); err != nil {
			return err
		}
		return s.cursors.SetSyncCursorTx(ctx, tx, state.ChannelID, watermark, state.Pts)
	})
	if err != nil {
		return fmt.Errorf("scan: commit history page of channel %d: %w", state.ChannelID, err)
	}
	return nil
}

func (s *Scanner) storeSampledPts(ctx context.Context, state syncer.ChannelState, watermark int64, pts int) error {
	if pts <= state.Pts {
		return nil
	}
	err := s.writer.Write(ctx, func(tx *sql.Tx) error {
		return s.cursors.SetSyncCursorTx(ctx, tx, state.ChannelID, watermark, pts)
	})
	if err != nil {
		return fmt.Errorf("scan: store pts of channel %d: %w", state.ChannelID, err)
	}
	return nil
}
