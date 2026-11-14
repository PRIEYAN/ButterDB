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
}

type WriteCoordinator struct {
	clock   clock.Clock
	sleeper Sleeper

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
	return &WriteCoordinator{
		clock:        deps.Clock,
		sleeper:      deps.Sleeper,
		messageSlot:  make(chan struct{}, 1),
		documentSlot: make(chan struct{}, 1),
	}, nil
}

func (c *WriteCoordinator) Do(ctx context.Context, class tgclient.WriteClass, action func() error) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidWriteRequest)
	}
	if action == nil {
		return fmt.Errorf("%w: action is required", ErrInvalidWriteRequest)
	}
	slot, err := c.slotFor(class)
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
	if err := c.waitForCooldown(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	actionErr := ConvertFloodWait(action())
	c.observe(actionErr)
	return actionErr
}

func (c *WriteCoordinator) slotFor(class tgclient.WriteClass) (chan struct{}, error) {
	switch class {
	case tgclient.WriteControl, tgclient.WriteDelete, tgclient.WriteChannel:
		return c.messageSlot, nil
	case tgclient.WriteDocument:
		return c.documentSlot, nil
	default:
		return nil, fmt.Errorf("%w: unknown write class %d", ErrInvalidWriteRequest, class)
	}
}

func (c *WriteCoordinator) waitForCooldown(ctx context.Context) error {
	for {
		c.mu.Lock()
		wait := c.blockedUntil.Sub(c.clock.Now())
		c.mu.Unlock()
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
	c.mu.Lock()
	if deadline.After(c.blockedUntil) {
		c.blockedUntil = deadline
	}
	c.mu.Unlock()
}
