package admission

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	sendRateReason  = "send rate limit"
	floodWaitReason = "telegram flood wait"
)

type Pacer interface {
	Reserve(class flow.SendClass) (delay time.Duration, cancel func())
}

func (c *Controller) WaitSend(ctx context.Context, b Budget, class flow.SendClass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay, cancel := c.pacer.Reserve(class)
	if delay <= 0 {
		return nil
	}
	if c.exceedsBudget(b, delay) {
		cancel()
		return newSlowDown(sendRateReason, delay)
	}
	if err := c.sleeper.Sleep(ctx, delay); err != nil {
		cancel()
		return contextErrorOr(ctx, err)
	}
	return nil
}

func (c *Controller) WaitFlood(ctx context.Context, b Budget, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if wait <= 0 {
		return nil
	}
	if c.exceedsBudget(b, wait) {
		return newSlowDown(floodWaitReason, wait)
	}
	if err := c.sleeper.Sleep(ctx, wait); err != nil {
		return contextErrorOr(ctx, err)
	}
	return nil
}

func (c *Controller) exceedsBudget(b Budget, wait time.Duration) bool {
	return c.clock.Now().Add(wait).After(b.Deadline)
}

func contextErrorOr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
