package flow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var ErrInvalidWriteRequest = errors.New("flow: invalid write request")

type WriteCoordinatorDeps struct {
	Clock   clock.Clock
	Sleeper Sleeper
	Limiter *SendLimiter
}

type WriteCoordinator struct {
	clock   clock.Clock
	sleeper Sleeper
	limiter *SendLimiter

	prepaced bool
	shared   *coordinatorState
}

type coordinatorState struct {
	messageSlot  chan struct{}
	documentSlot chan struct{}

	mu           sync.Mutex
	blockedUntil time.Time
}

func NewWriteCoordinator(deps WriteCoordinatorDeps) (*WriteCoordinator, error) {
	if deps.Clock == nil {
		return nil, fmt.Errorf("flow: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Sleeper == nil {
		return nil, fmt.Errorf("flow: %w: sleeper", errs.ErrMissingDependency)
	}
	if deps.Limiter == nil {
		return nil, fmt.Errorf("flow: %w: limiter", errs.ErrMissingDependency)
	}
	return &WriteCoordinator{
		clock:   deps.Clock,
		sleeper: deps.Sleeper,
		limiter: deps.Limiter,
		shared: &coordinatorState{
			messageSlot:  make(chan struct{}, 1),
			documentSlot: make(chan struct{}, 1),
		},
	}, nil
}

func (c *WriteCoordinator) Prepaced() *WriteCoordinator {
	view := *c
	view.prepaced = true
	return &view
}

func (c *WriteCoordinator) Do(ctx context.Context, class tgclient.WriteClass, action func() error) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidWriteRequest)
	}
	if action == nil {
		return fmt.Errorf("%w: action is required", ErrInvalidWriteRequest)
	}
	slot, sendClass, err := c.routeFor(class)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// A concurrent write or the preceding send may have extended the cooldown while this call queued.
	if err := c.waitForTurn(ctx, sendClass); err != nil {
		return err
	}
	actionErr := ConvertFloodWait(action())
	c.observe(actionErr)
	return actionErr
}

func (c *WriteCoordinator) routeFor(class tgclient.WriteClass) (chan struct{}, SendClass, error) {
	switch class {
	case tgclient.WriteControl, tgclient.WriteDelete, tgclient.WriteChannel:
		return c.shared.messageSlot, SendControl, nil
	case tgclient.WriteDocument:
		return c.shared.documentSlot, SendDocument, nil
	default:
		return nil, 0, fmt.Errorf("%w: unknown write class %d", ErrInvalidWriteRequest, class)
	}
}

func (c *WriteCoordinator) waitForTurn(ctx context.Context, sendClass SendClass) error {
	if err := c.waitForCooldown(ctx); err != nil {
		return err
	}
	if !c.prepaced {
		if err := c.limiter.Wait(ctx, sendClass); err != nil {
			return err
		}
		if err := c.waitForCooldown(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *WriteCoordinator) waitForCooldown(ctx context.Context) error {
	for {
		c.shared.mu.Lock()
		wait := c.shared.blockedUntil.Sub(c.clock.Now())
		c.shared.mu.Unlock()
		if wait <= 0 {
			return nil
		}
		if err := c.sleeper.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (c *WriteCoordinator) observe(err error) {
	wait, ok := FloodWaitDuration(err)
	if !ok || wait < 0 {
		return
	}
	deadline := c.clock.Now().Add(wait)
	c.shared.mu.Lock()
	if deadline.After(c.shared.blockedUntil) {
		c.shared.blockedUntil = deadline
	}
	c.shared.mu.Unlock()
}
