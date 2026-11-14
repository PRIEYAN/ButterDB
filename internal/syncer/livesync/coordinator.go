package livesync

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
)

const (
	DefaultDebounce         = 750 * time.Millisecond
	DefaultBackstopInterval = 5 * time.Minute
	DefaultSyncTimeout      = 2 * time.Minute
	DefaultPollInterval     = 50 * time.Millisecond
)

const (
	ReasonSignal   = "signal"
	ReasonBackstop = "backstop"
)

type ChannelSyncer interface {
	Sync(ctx context.Context, channelID int64) error
}

type ChannelLister interface {
	Channels(ctx context.Context) ([]syncer.ChannelState, error)
}

type SignalSource interface {
	Signals() <-chan int64
}

type Dependencies struct {
	Signals  SignalSource
	Syncer   ChannelSyncer
	Channels ChannelLister
	Clock    clock.Clock
	Logger   *slog.Logger
}

type Config struct {
	Debounce         time.Duration
	BackstopInterval time.Duration
	SyncTimeout      time.Duration
	PollInterval     time.Duration
}

type Coordinator struct {
	deps Dependencies
	cfg  Config

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCoordinator(deps Dependencies, cfg Config) (*Coordinator, error) {
	if deps.Signals == nil {
		return nil, fmt.Errorf("livesync: %w: signals", errs.ErrMissingDependency)
	}
	if deps.Syncer == nil {
		return nil, fmt.Errorf("livesync: %w: syncer", errs.ErrMissingDependency)
	}
	if deps.Channels == nil {
		return nil, fmt.Errorf("livesync: %w: channels", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("livesync: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("livesync: %w: logger", errs.ErrMissingDependency)
	}
	return &Coordinator{deps: deps, cfg: withDefaults(cfg)}, nil
}

func withDefaults(cfg Config) Config {
	if cfg.Debounce <= 0 {
		cfg.Debounce = DefaultDebounce
	}
	if cfg.BackstopInterval <= 0 {
		cfg.BackstopInterval = DefaultBackstopInterval
	}
	if cfg.SyncTimeout <= 0 {
		cfg.SyncTimeout = DefaultSyncTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	return cfg
}

func (c *Coordinator) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("livesync: start: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	c.deps.Logger.Info("livesync: coordinator starting", "debounce", c.cfg.Debounce, "backstop_interval", c.cfg.BackstopInterval)
	go c.run(runCtx, done)
	return nil
}

func (c *Coordinator) Close(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.done = nil
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		c.deps.Logger.Info("livesync: coordinator stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("livesync: close: %w", ctx.Err())
	}
}

type loopState struct {
	known        map[int64]bool
	pending      map[int64]string
	flushAt      time.Time
	nextBackstop time.Time
}

func (c *Coordinator) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	signals := c.deps.Signals.Signals()
	state := &loopState{
		known:        c.loadKnown(ctx, nil),
		pending:      make(map[int64]string),
		nextBackstop: c.deps.Clock.Now().Add(c.cfg.BackstopInterval),
	}
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case channelID, ok := <-signals:
			if !ok {
				return
			}
			c.handleSignal(ctx, state, channelID)
		case <-ticker.C:
			c.handleTick(ctx, state)
		}
	}
}

func (c *Coordinator) handleSignal(ctx context.Context, state *loopState, channelID int64) {
	if channelID <= 0 {
		return
	}
	if !state.known[channelID] {
		state.known = c.loadKnown(ctx, state.known)
		if !state.known[channelID] {
			return
		}
	}
	state.pending[channelID] = ReasonSignal
	state.flushAt = c.deps.Clock.Now().Add(c.cfg.Debounce)
}

func (c *Coordinator) handleTick(ctx context.Context, state *loopState) {
	now := c.deps.Clock.Now()
	if !now.Before(state.nextBackstop) {
		c.markBackstop(ctx, state, now)
	}
	if len(state.pending) == 0 || now.Before(state.flushAt) {
		return
	}
	pending := state.pending
	state.pending = make(map[int64]string)
	c.flush(ctx, pending)
}

func (c *Coordinator) markBackstop(ctx context.Context, state *loopState, now time.Time) {
	state.nextBackstop = now.Add(c.cfg.BackstopInterval)
	state.known = c.loadKnown(ctx, state.known)
	for channelID := range state.known {
		if _, queued := state.pending[channelID]; !queued {
			state.pending[channelID] = ReasonBackstop
		}
	}
	if len(state.pending) > 0 {
		state.flushAt = now
	}
}

func (c *Coordinator) flush(ctx context.Context, pending map[int64]string) {
	ids := make([]int64, 0, len(pending))
	for channelID := range pending {
		ids = append(ids, channelID)
	}
	slices.Sort(ids)
	for _, channelID := range ids {
		if ctx.Err() != nil {
			return
		}
		c.syncOne(ctx, channelID, pending[channelID])
	}
}

func (c *Coordinator) syncOne(ctx context.Context, channelID int64, reason string) {
	logger := c.deps.Logger.With("channel_id", channelID, "reason", reason)
	logger.Debug("livesync: sync starting")
	syncCtx, cancel := context.WithTimeout(ctx, c.cfg.SyncTimeout)
	defer cancel()
	if err := c.deps.Syncer.Sync(syncCtx, channelID); err != nil {
		logger.Warn("livesync: sync failed", "error", err)
		return
	}
	logger.Debug("livesync: sync complete")
}

func (c *Coordinator) loadKnown(ctx context.Context, previous map[int64]bool) map[int64]bool {
	channels, err := c.deps.Channels.Channels(ctx)
	if err != nil {
		c.deps.Logger.Warn("livesync: list channels failed", "error", err)
		if previous == nil {
			return make(map[int64]bool)
		}
		return previous
	}
	known := make(map[int64]bool, len(channels))
	for _, channel := range channels {
		if channel.ChannelID > 0 && !channel.Deleted {
			known[channel.ChannelID] = true
		}
	}
	return known
}
