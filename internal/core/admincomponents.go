package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	controlSocketReadyPolls = 200
	controlSocketPollEvery  = 10 * time.Millisecond
	controlSocketReadyMode  = 0o600
)

var (
	_ lifecycle.Component = (*controlSocket)(nil)
	_ lifecycle.Component = (*adminSweep)(nil)
)

type controlSocket struct {
	path    string
	handler session.ControlHandler
	sleeper flow.Sleeper
	logger  *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan error
}

func newControlSocket(path string, handler session.ControlHandler, sleeper flow.Sleeper, logger *slog.Logger) *controlSocket {
	return &controlSocket{path: path, handler: handler, sleeper: sleeper, logger: logger}
}

func (c *controlSocket) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- session.ServeControlSocket(runCtx, c.path, c.handler) }()
	if err := c.awaitReady(ctx, done); err != nil {
		cancel()
		return err
	}
	c.cancel, c.done = cancel, done
	c.logger.Info("admin control socket", "component", "admin", "path", c.path)
	return nil
}

func (c *controlSocket) awaitReady(ctx context.Context, done <-chan error) error {
	for poll := 0; poll < controlSocketReadyPolls; poll++ {
		select {
		case err := <-done:
			return fmt.Errorf("core: admin: control socket: %w", errors.Join(err, errControlSocketStopped))
		default:
		}
		if info, err := os.Stat(c.path); err == nil && info.Mode()&fs.ModeSocket != 0 && info.Mode().Perm() == controlSocketReadyMode {
			return nil
		}
		if err := c.sleeper.Sleep(ctx, controlSocketPollEvery); err != nil {
			return fmt.Errorf("core: admin: control socket: %w", err)
		}
	}
	return fmt.Errorf("core: admin: control socket: %w", errControlSocketTimeout)
}

var (
	errControlSocketStopped = errors.New("stopped before it was ready")
	errControlSocketTimeout = errors.New("did not become ready")
)

func (c *controlSocket) Close(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("core: admin: control socket: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("core: admin: control socket: stop: %w", ctx.Err())
	}
}

type sweepRunner interface {
	RunOnce(ctx context.Context) error
}

type adminSweep struct {
	sweeper sweepRunner
	every   time.Duration
	sleeper flow.Sleeper
	logger  *slog.Logger
	loop    backgroundLoop
}

func newAdminSweep(sweeper sweepRunner, every time.Duration, sleeper flow.Sleeper, logger *slog.Logger) *adminSweep {
	return &adminSweep{sweeper: sweeper, every: every, sleeper: sleeper, logger: logger}
}

func (a *adminSweep) Start(ctx context.Context) error {
	a.loop.launch(ctx, a.run)
	return nil
}

func (a *adminSweep) run(ctx context.Context) {
	for {
		if err := a.sweeper.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.logger.Warn("admin sweep failed", "component", "admin", "error", err)
		}
		if a.sleeper.Sleep(ctx, a.every) != nil {
			return
		}
	}
}

func (a *adminSweep) Close(ctx context.Context) error {
	return a.loop.stop(ctx, "admin sweep")
}
