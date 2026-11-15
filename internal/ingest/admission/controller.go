package admission

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const putSlotsBusyReason = "put slots busy"

type Dependencies struct {
	Config  Config
	Pacer   Pacer
	Clock   clock.Clock
	Sleeper Sleeper
}

type Controller struct {
	pacer   Pacer
	clock   clock.Clock
	sleeper Sleeper
	slots   *slotTable
}

func New(deps Dependencies) (*Controller, error) {
	if deps.Pacer == nil {
		return nil, fmt.Errorf("admission: %w: pacer", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("admission: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Config.MaxConcurrentPuts < 1 {
		return nil, fmt.Errorf("admission: max concurrent puts must be at least 1, got %d", deps.Config.MaxConcurrentPuts)
	}
	if deps.Config.MaxConcurrentPutsPerKey < 1 {
		return nil, fmt.Errorf("admission: max concurrent puts per key must be at least 1, got %d", deps.Config.MaxConcurrentPutsPerKey)
	}
	sleeper := deps.Sleeper
	if sleeper == nil {
		sleeper = timerSleeper{}
	}
	return &Controller{
		pacer:   deps.Pacer,
		clock:   deps.Clock,
		sleeper: sleeper,
		slots:   newSlotTable(deps.Config.MaxConcurrentPuts, deps.Config.MaxConcurrentPutsPerKey),
	}, nil
}

func (c *Controller) AcquirePut(ctx context.Context, b Budget, scope string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.slots.tryAcquire(scope) {
		return c.releaseOnce(scope), nil
	}
	remaining := b.Remaining(c.clock.Now())
	if remaining <= 0 {
		return nil, newSlowDown(putSlotsBusyReason, 0)
	}
	if err := c.waitForSlot(ctx, scope, remaining); err != nil {
		return nil, err
	}
	return c.releaseOnce(scope), nil
}

func (c *Controller) releaseOnce(scope string) func() {
	var once sync.Once
	return func() {
		once.Do(func() { c.slots.release(scope) })
	}
}

func (c *Controller) waitForSlot(ctx context.Context, scope string, remaining time.Duration) error {
	waiter := c.slots.enqueue(scope)
	budgetExpired := c.budgetTimer(ctx, remaining)
	defer budgetExpired.stop()
	select {
	case <-waiter.ready:
		return nil
	case <-ctx.Done():
		c.returnIfGranted(waiter)
		return ctx.Err()
	case <-budgetExpired.fired:
		if c.slots.abandon(waiter) {
			return nil
		}
		return newSlowDown(putSlotsBusyReason, 0)
	}
}

func (c *Controller) returnIfGranted(waiter *slotWaiter) {
	if c.slots.abandon(waiter) {
		c.slots.release(waiter.key)
	}
}

type budgetTimer struct {
	fired  <-chan struct{}
	cancel context.CancelFunc
	done   <-chan struct{}
}

func (c *Controller) budgetTimer(ctx context.Context, remaining time.Duration) *budgetTimer {
	timerCtx, cancel := context.WithCancel(ctx)
	fired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if c.sleeper.Sleep(timerCtx, remaining) == nil {
			close(fired)
		}
	}()
	return &budgetTimer{fired: fired, cancel: cancel, done: done}
}

func (t *budgetTimer) stop() {
	t.cancel()
	<-t.done
}
