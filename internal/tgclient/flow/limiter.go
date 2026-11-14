package flow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

type SendClass int

const (
	SendControl SendClass = iota + 1
	SendDocument
)

type SendLimiter struct {
	clock     clock.Clock
	sleeper   Sleeper
	control   *rate.Limiter
	documents *rate.Limiter
}

func NewSendLimiter(controlPerMinute, controlBurst, docdocumentsPerMinute, docdocumentsBurst int) *SendLimiter {
	return newSendLimiter(clock.System{}, SystemSleeper{}, controlPerMinute, controlBurst, docdocumentsPerMinute, docdocumentsBurst)
}

func NewSendLimiterWithClock(clk clock.Clock, sleeper Sleeper, controlPerMinute, controlBurst, docdocumentsPerMinute, docdocumentsBurst int) (*SendLimiter, error) {
	if clk == nil {
		return nil, fmt.Errorf("flow: %w: clock", errs.ErrMissingDependency)
	}
	if sleeper == nil {
		return nil, fmt.Errorf("flow: %w: sleeper", errs.ErrMissingDependency)
	}
	return newSendLimiter(clk, sleeper, controlPerMinute, controlBurst, docdocumentsPerMinute, docdocumentsBurst), nil
}

func newSendLimiter(clk clock.Clock, sleeper Sleeper, controlPerMinute, controlBurst, docdocumentsPerMinute, docdocumentsBurst int) *SendLimiter {
	return &SendLimiter{
		clock:     clk,
		sleeper:   sleeper,
		control:   newClassLimiter(controlPerMinute, controlBurst),
		documents: newClassLimiter(docdocumentsPerMinute, docdocumentsBurst),
	}
}

func newClassLimiter(perMinute, burst int) *rate.Limiter {
	perMinute = max(perMinute, 1)
	burst = max(burst, 1)
	return rate.NewLimiter(rate.Limit(float64(perMinute)/60), burst)
}

func (l *SendLimiter) limiterFor(class SendClass) *rate.Limiter {
	if class == SendDocument {
		return l.documents
	}
	return l.control
}

func (l *SendLimiter) Reserve(class SendClass) (time.Duration, func()) {
	now := l.clock.Now()
	reservation := l.limiterFor(class).ReserveN(now, 1)
	var once sync.Once
	cancel := func() {
		once.Do(func() { reservation.CancelAt(l.clock.Now()) })
	}
	return reservation.DelayFrom(now), cancel
}

func (l *SendLimiter) Wait(ctx context.Context, class SendClass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay, cancel := l.Reserve(class)
	if delay <= 0 {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && l.clock.Now().Add(delay).After(deadline) {
		cancel()
		return fmt.Errorf("flow: send limiter wait of %s exceeds the context deadline: %w", delay, context.DeadlineExceeded)
	}
	if err := l.sleeper.Sleep(ctx, delay); err != nil {
		cancel()
		return err
	}
	return nil
}
