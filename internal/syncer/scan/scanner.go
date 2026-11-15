package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var _ syncer.HistoryScanner = (*Scanner)(nil)

type PeerResolver interface {
	ResolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type HistoryReader interface {
	GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error)
	GetChannelPts(ctx context.Context, peer tgclient.InputPeer) (int, error)
}

type Rebuilder interface {
	RebuildChannelTx(ctx context.Context, tx projection.Tx, channelID int64) (rebuild.Result, error)
	RebuildAllTx(ctx context.Context, tx projection.Tx) (rebuild.Result, error)
}

type Sleeper interface {
	Sleep(ctx context.Context, wait time.Duration) error
}

type Dependencies struct {
	Channels  syncer.ChannelStore
	Cursors   syncer.SyncCursorStore
	Spool     syncer.ScanSpool
	Ingester  syncer.OpIngester
	Rebuilder Rebuilder
	Writer    projection.Writer
	Peers     PeerResolver
	History   HistoryReader
	Gate      syncer.ChannelGate
	Sleeper   Sleeper
	Clock     clock.Clock
	Random    io.Reader
	Logger    *slog.Logger
}

type Scanner struct {
	channels  syncer.ChannelStore
	cursors   syncer.SyncCursorStore
	spool     syncer.ScanSpool
	ingester  syncer.OpIngester
	rebuilder Rebuilder
	writer    projection.Writer
	peers     PeerResolver
	history   HistoryReader
	gate      syncer.ChannelGate
	sleeper   Sleeper
	clock     clock.Clock
	random    io.Reader
	logger    *slog.Logger
}

func NewScanner(deps Dependencies) (*Scanner, error) {
	missing := missingDependency(deps)
	if missing != "" {
		return nil, fmt.Errorf("scan: %w: %s", errs.ErrMissingDependency, missing)
	}
	return &Scanner{
		channels:  deps.Channels,
		cursors:   deps.Cursors,
		spool:     deps.Spool,
		ingester:  deps.Ingester,
		rebuilder: deps.Rebuilder,
		writer:    deps.Writer,
		peers:     deps.Peers,
		history:   deps.History,
		gate:      deps.Gate,
		sleeper:   deps.Sleeper,
		clock:     deps.Clock,
		random:    deps.Random,
		logger:    deps.Logger,
	}, nil
}

func missingDependency(deps Dependencies) string {
	switch {
	case deps.Channels == nil:
		return "channels"
	case deps.Cursors == nil:
		return "cursors"
	case deps.Spool == nil:
		return "spool"
	case deps.Ingester == nil:
		return "ingester"
	case deps.Rebuilder == nil:
		return "rebuilder"
	case deps.Writer == nil:
		return "writer"
	case deps.Peers == nil:
		return "peers"
	case deps.History == nil:
		return "history"
	case deps.Gate == nil:
		return "gate"
	case deps.Sleeper == nil:
		return "sleeper"
	case deps.Clock == nil:
		return "clock"
	case deps.Random == nil:
		return "random"
	case deps.Logger == nil:
		return "logger"
	}
	return ""
}

func (s *Scanner) EnsureAuthoritative(ctx context.Context, channelID int64) error {
	state, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if state.InitialSyncDone && !state.NeedsProjectionRebuild {
		return s.Incremental(ctx, channelID)
	}
	return s.FullScan(ctx, channelID)
}

func (s *Scanner) EnsureAllAuthoritative(ctx context.Context) error {
	channels, err := s.channels.Channels(ctx)
	if err != nil {
		return fmt.Errorf("scan: list channels: %w", err)
	}
	registry, buckets := splitChannels(channels)
	if len(registry) == 0 && len(buckets) == 0 {
		return nil
	}
	if len(registry) == 0 {
		return fmt.Errorf("scan: registry channel: %w", syncer.ErrUnknownChannel)
	}
	if err := s.ensureUnderGate(ctx, registry[0].ChannelID); err != nil {
		return fmt.Errorf("scan: registry channel %d: %w", registry[0].ChannelID, err)
	}
	return s.ensureBuckets(ctx)
}

func (s *Scanner) ensureBuckets(ctx context.Context) error {
	// Buckets are listed again because the registry scan is what discovers them.
	channels, err := s.channels.Channels(ctx)
	if err != nil {
		return fmt.Errorf("scan: list channels: %w", err)
	}
	_, buckets := splitChannels(channels)
	var failures []error
	for _, bucket := range buckets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.ensureUnderGate(ctx, bucket.ChannelID); err != nil {
			failures = append(failures, fmt.Errorf("scan: bucket channel %d: %w", bucket.ChannelID, err))
		}
	}
	return errors.Join(failures...)
}

func splitChannels(channels []syncer.ChannelState) (registry, buckets []syncer.ChannelState) {
	for _, channel := range channels {
		switch {
		case channel.Deleted:
		case channel.Kind == syncer.KindRegistry:
			registry = append(registry, channel)
		default:
			buckets = append(buckets, channel)
		}
	}
	return registry, buckets
}

func (s *Scanner) ensureUnderGate(ctx context.Context, channelID int64) error {
	release, err := s.gate.Acquire(ctx, channelID)
	if err != nil {
		return err
	}
	defer release()
	return s.EnsureAuthoritative(ctx, channelID)
}

func (s *Scanner) loadChannel(ctx context.Context, channelID int64) (syncer.ChannelState, error) {
	state, found, err := s.channels.Channel(ctx, channelID)
	if err != nil {
		return syncer.ChannelState{}, fmt.Errorf("scan: read channel %d: %w", channelID, err)
	}
	if !found || state.Deleted {
		return syncer.ChannelState{}, fmt.Errorf("scan: channel %d: %w", channelID, syncer.ErrUnknownChannel)
	}
	return state, nil
}

func (s *Scanner) resolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	peer, err := s.peers.ResolvePeer(ctx, channelID)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("scan: resolve peer for channel %d: %w", channelID, err)
	}
	return peer, nil
}

func (s *Scanner) InitialSyncEmptyChannel(ctx context.Context, channelID int64) error {
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
	err = s.writer.Write(ctx, func(tx *sql.Tx) error {
		empty, err := projlog.ChannelIsEmpty(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("scan: channel %d: %w", channelID, syncer.ErrChannelNotEmpty)
		}
		return s.cursors.MarkInitialSyncDoneTx(ctx, tx, channelID, 0, newestPts(pts, state.Pts))
	})
	if err != nil {
		return err
	}
	s.logger.Info("scan: empty channel marked authoritative", "channel_id", channelID, "pts", pts)
	return nil
}

func newestPts(sampled, stored int) int {
	return max(sampled, stored)
}
