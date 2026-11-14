package flow

import (
	"context"
	"time"
)

type Sleeper interface {
	Sleep(ctx context.Context, wait time.Duration) error
}

type SleeperFunc func(ctx context.Context, wait time.Duration) error

func (f SleeperFunc) Sleep(ctx context.Context, wait time.Duration) error {
	return f(ctx, wait)
}

type SystemSleeper struct{}

func (SystemSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
