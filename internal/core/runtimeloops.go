package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	authoritativeRetryWait = 30 * time.Second
	multipartExpiryEvery   = time.Hour
)

var (
	_ lifecycle.Component = (*authoritativeSync)(nil)
	_ lifecycle.Component = (*multipartExpiry)(nil)
)

type authoritativeScanner interface {
	EnsureAllAuthoritative(ctx context.Context) error
}

type serverReadiness interface {
	MarkReady()
	MarkStarting()
}

type backgroundLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (l *backgroundLoop) launch(ctx context.Context, run func(ctx context.Context)) {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l.cancel = cancel
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		run(runCtx)
	}()
}

func (l *backgroundLoop) stop(ctx context.Context, name string) error {
	if l.cancel == nil {
		return nil
	}
	l.cancel()
	select {
	case <-l.done:
		l.cancel = nil
		return nil
	case <-ctx.Done():
		return fmt.Errorf("core: %s: stop: %w", name, ctx.Err())
	}
}

type authoritativeSync struct {
	scanner   authoritativeScanner
	readiness serverReadiness
	sleeper   flow.Sleeper
	logger    *slog.Logger
	loop      backgroundLoop
}

func (a *authoritativeSync) Start(ctx context.Context) error {
	a.readiness.MarkStarting()
	a.loop.launch(ctx, a.run)
	return nil
}

func (a *authoritativeSync) run(ctx context.Context) {
	for {
		err := a.scanner.EnsureAllAuthoritative(ctx)
		if err == nil {
			a.readiness.MarkReady()
			a.logger.Info("initial sync complete", "component", "sync")
			return
		}
		if ctx.Err() != nil {
			return
		}
		a.logger.Warn("initial sync failed; retrying", "component", "sync", "error", err)
		if a.sleeper.Sleep(ctx, authoritativeRetryWait) != nil {
			return
		}
	}
}

func (a *authoritativeSync) Close(ctx context.Context) error {
	return a.loop.stop(ctx, "initial sync")
}

type idleUploadSweeper interface {
	SweepIdle(ctx context.Context, idle time.Duration) (int, error)
}

type multipartExpiry struct {
	stage   idleUploadSweeper
	idle    time.Duration
	sleeper flow.Sleeper
	logger  *slog.Logger
	loop    backgroundLoop
}

func (m *multipartExpiry) Start(ctx context.Context) error {
	m.loop.launch(ctx, m.run)
	return nil
}

func (m *multipartExpiry) run(ctx context.Context) {
	for {
		aborted, err := m.stage.SweepIdle(ctx, m.idle)
		if err != nil && ctx.Err() == nil {
			m.logger.Warn("multipart expiry sweep failed", "component", "multipart", "error", err)
		}
		if aborted > 0 {
			m.logger.Info("idle multipart uploads aborted", "component", "multipart", "count", aborted)
		}
		if m.sleeper.Sleep(ctx, multipartExpiryEvery) != nil {
			return
		}
	}
}

func (m *multipartExpiry) Close(ctx context.Context) error {
	return m.loop.stop(ctx, "multipart expiry")
}
