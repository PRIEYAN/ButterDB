package flow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

type advancingSleeper struct {
	clock *clock.Fake
	mu    sync.Mutex
	slept time.Duration
}

func (s *advancingSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.slept += wait
	s.mu.Unlock()
	s.clock.Advance(wait)
	return nil
}

func (s *advancingSleeper) total() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slept
}

func newTestLimiter(t *testing.T, controlPerMinute, controlBurst, documentsPerMinute, documentsBurst int) (*SendLimiter, *clock.Fake, *advancingSleeper) {
	t.Helper()
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	sleeper := &advancingSleeper{clock: fake}
	limiter, err := NewSendLimiterWithClock(fake, sleeper, controlPerMinute, controlBurst, documentsPerMinute, documentsBurst)
	if err != nil {
		t.Fatalf("NewSendLimiterWithClock: %v", err)
	}
	return limiter, fake, sleeper
}

func TestSendLimiterBurstThenSteady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		class SendClass
		want  []time.Duration
	}{
		{
			"control burst of five then one every three seconds",
			SendControl,
			[]time.Duration{0, 0, 0, 0, 0, 3 * time.Second, 6 * time.Second, 9 * time.Second},
		},
		{
			"document burst of one then one every ten seconds",
			SendDocument,
			[]time.Duration{0, 10 * time.Second, 20 * time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter, _, _ := newTestLimiter(t, 20, 5, 6, 1)
			for i, want := range tt.want {
				delay, _ := limiter.Reserve(tt.class)
				if delay != want {
					t.Fatalf("reservation %d delay = %s, want %s", i, delay, want)
				}
			}
		})
	}
}

func TestSendLimiterClassesAreIndependent(t *testing.T) {
	t.Parallel()

	limiter, _, _ := newTestLimiter(t, 20, 1, 6, 1)
	if delay, _ := limiter.Reserve(SendDocument); delay != 0 {
		t.Fatalf("first document delay = %s, want 0", delay)
	}
	if delay, _ := limiter.Reserve(SendControl); delay != 0 {
		t.Fatalf("first control delay after a document = %s, want 0", delay)
	}
}

func TestSendLimiterRefillsWithTime(t *testing.T) {
	t.Parallel()

	limiter, fake, _ := newTestLimiter(t, 20, 1, 6, 1)
	limiter.Reserve(SendControl)
	if delay, _ := limiter.Reserve(SendControl); delay != 3*time.Second {
		t.Fatalf("second control delay = %s, want 3s", delay)
	}
	fake.Advance(30 * time.Second)
	if delay, _ := limiter.Reserve(SendControl); delay != 0 {
		t.Fatalf("control delay after refill = %s, want 0", delay)
	}
}

func TestReserveCancelReturnsToken(t *testing.T) {
	t.Parallel()

	limiter, _, _ := newTestLimiter(t, 20, 5, 6, 1)
	if delay, _ := limiter.Reserve(SendDocument); delay != 0 {
		t.Fatalf("first document delay = %s, want 0", delay)
	}
	delay, cancel := limiter.Reserve(SendDocument)
	if delay != 10*time.Second {
		t.Fatalf("second document delay = %s, want 10s", delay)
	}
	cancel()
	cancel()
	if delay, _ := limiter.Reserve(SendDocument); delay != 10*time.Second {
		t.Fatalf("third document delay after cancel = %s, want 10s", delay)
	}
}

func TestSendLimiterWaitSleepsForTheDelay(t *testing.T) {
	t.Parallel()

	limiter, _, sleeper := newTestLimiter(t, 20, 1, 6, 1)
	if err := limiter.Wait(context.Background(), SendControl); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if err := limiter.Wait(context.Background(), SendControl); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if got := sleeper.total(); got != 3*time.Second {
		t.Fatalf("slept %s, want 3s", got)
	}
}

func TestSendLimiterWaitCancellationReturnsToken(t *testing.T) {
	t.Parallel()

	limiter, _, _ := newTestLimiter(t, 20, 1, 6, 1)
	limiter.Reserve(SendControl)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx, SendControl); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context canceled", err)
	}
	if delay, _ := limiter.Reserve(SendControl); delay != 3*time.Second {
		t.Fatalf("delay after canceled wait = %s, want 3s", delay)
	}
}

func TestSendLimiterWaitRefusesWhenDeadlineIsTooSoon(t *testing.T) {
	t.Parallel()

	limiter, fake, _ := newTestLimiter(t, 20, 1, 6, 1)
	limiter.Reserve(SendDocument)
	ctx, cancel := context.WithDeadline(context.Background(), fake.Now().Add(time.Second))
	defer cancel()
	if err := limiter.Wait(ctx, SendDocument); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want deadline exceeded", err)
	}
	if delay, _ := limiter.Reserve(SendDocument); delay != 10*time.Second {
		t.Fatalf("delay after refused wait = %s, want 10s", delay)
	}
}

func TestSendLimiterNeverRunsUnlimited(t *testing.T) {
	t.Parallel()

	limiter := NewSendLimiter(0, 0, -5, 0)
	for _, class := range []SendClass{SendControl, SendDocument} {
		if delay, _ := limiter.Reserve(class); delay != 0 {
			t.Fatalf("class %d first delay = %s, want 0", class, delay)
		}
		if delay, _ := limiter.Reserve(class); delay <= 0 {
			t.Fatalf("class %d second delay = %s, want a positive delay", class, delay)
		}
	}
}

func TestNewSendLimiterWithClockRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewSendLimiterWithClock(nil, SystemSleeper{}, 20, 5, 6, 1); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil clock error = %v, want missing dependency", err)
	}
	if _, err := NewSendLimiterWithClock(clock.System{}, nil, 20, 5, 6, 1); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil sleeper error = %v, want missing dependency", err)
	}
}
