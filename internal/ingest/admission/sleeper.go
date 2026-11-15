package admission

import (
	"context"
	"time"
)

type Sleeper interface {
	Sleep(ctx context.Context, wait time.Duration) error
}

type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, wait time.Duration) error {
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
