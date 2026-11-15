package admission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

type fakePacer struct {
	mu        sync.Mutex
	delay     time.Duration
	reserved  []flow.SendClass
	cancelled int
}

func (p *fakePacer) Reserve(class flow.SendClass) (time.Duration, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reserved = append(p.reserved, class)
	return p.delay, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.cancelled++
	}
}

func (p *fakePacer) counts() (reserved, cancelled int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reserved), p.cancelled
}

func newPacedController(t *testing.T, pacer Pacer, sleeper Sleeper) (*Controller, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(testEpoch)
	c, err := New(Dependencies{
		Config:  Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1},
		Pacer:   pacer,
		Clock:   clk,
		Sleeper: sleeper,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, clk
}

func TestSendReservationCancelledOnSlowDown(t *testing.T) {
	tests := []struct {
		name          string
		delay         time.Duration
		budget        time.Duration
		wantSlowDown  bool
		wantCancelled int
		wantRetry     time.Duration
		wantSleeps    int
	}{
		{"delay past deadline cancels", 45 * time.Second, 30 * time.Second, true, 1, 45 * time.Second, 0},
		{"fractional delay rounds up", 31500 * time.Millisecond, 30 * time.Second, true, 1, 32 * time.Second, 0},
		{"delay exactly at deadline sleeps", 30 * time.Second, 30 * time.Second, false, 0, 0, 1},
		{"no delay does not sleep", 0, 30 * time.Second, false, 0, 0, 0},
		{"delay with exhausted budget cancels", time.Millisecond, 0, true, 1, time.Second, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pacer := &fakePacer{delay: tt.delay}
			sleeper := &instantSleeper{}
			c, clk := newPacedController(t, pacer, sleeper)
			sleeper.clock = clk
			budget := NewBudget(clk.Now(), tt.budget)

			err := c.WaitSend(context.Background(), budget, flow.SendDocument)

			var slow *SlowDownError
			if tt.wantSlowDown {
				if !errors.As(err, &slow) {
					t.Fatalf("err = %v, want *SlowDownError", err)
				}
				if slow.RetryAfter != tt.wantRetry {
					t.Fatalf("RetryAfter = %s, want %s", slow.RetryAfter, tt.wantRetry)
				}
			} else if err != nil {
				t.Fatalf("WaitSend: %v", err)
			}
			reserved, cancelled := pacer.counts()
			if reserved != 1 {
				t.Fatalf("reservations = %d, want 1", reserved)
			}
			if cancelled != tt.wantCancelled {
				t.Fatalf("cancelled = %d, want %d", cancelled, tt.wantCancelled)
			}
			if got := len(sleeper.recorded()); got != tt.wantSleeps {
				t.Fatalf("sleeps = %d, want %d", got, tt.wantSleeps)
			}
			if pacer.reserved[0] != flow.SendDocument {
				t.Fatalf("class = %v, want SendDocument", pacer.reserved[0])
			}
		})
	}
}

func TestFloodWaitBeyondBudget(t *testing.T) {
	tests := []struct {
		name      string
		budget    time.Duration
		wait      time.Duration
		wantRetry time.Duration
	}{
		{"wait longer than budget", 30 * time.Second, 120 * time.Second, 120 * time.Second},
		{"one nanosecond past budget", 30 * time.Second, 30*time.Second + time.Nanosecond, 31 * time.Second},
		{"short wait with nothing left", 0, 400 * time.Millisecond, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sleeper := &instantSleeper{}
			c, clk := newPacedController(t, &fakePacer{}, sleeper)
			sleeper.clock = clk

			err := c.WaitFlood(context.Background(), NewBudget(clk.Now(), tt.budget), tt.wait)

			var slow *SlowDownError
			if !errors.As(err, &slow) {
				t.Fatalf("err = %v, want *SlowDownError", err)
			}
			if slow.RetryAfter != tt.wantRetry {
				t.Fatalf("RetryAfter = %s, want %s", slow.RetryAfter, tt.wantRetry)
			}
			if !errors.Is(err, errs.ErrSlowDown) {
				t.Fatal("error does not match errs.ErrSlowDown")
			}
			if len(sleeper.recorded()) != 0 {
				t.Fatal("slept despite the budget being exceeded")
			}
		})
	}
}

func TestFloodWaitWithinBudgetSleeps(t *testing.T) {
	tests := []struct {
		name       string
		wait       time.Duration
		wantSleeps []time.Duration
	}{
		{"short wait sleeps", 5 * time.Second, []time.Duration{5 * time.Second}},
		{"wait exactly the budget sleeps", 30 * time.Second, []time.Duration{30 * time.Second}},
		{"zero wait does not sleep", 0, nil},
		{"negative wait does not sleep", -time.Second, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sleeper := &instantSleeper{}
			c, clk := newPacedController(t, &fakePacer{}, sleeper)
			sleeper.clock = clk
			start := clk.Now()

			if err := c.WaitFlood(context.Background(), NewBudget(start, 30*time.Second), tt.wait); err != nil {
				t.Fatalf("WaitFlood: %v", err)
			}
			got := sleeper.recorded()
			if len(got) != len(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", got, tt.wantSleeps)
			}
			for i := range got {
				if got[i] != tt.wantSleeps[i] {
					t.Fatalf("sleeps = %v, want %v", got, tt.wantSleeps)
				}
			}
		})
	}
}

func TestSecondWaitSeesBudgetSpentByFirst(t *testing.T) {
	pacer := &fakePacer{delay: 20 * time.Second}
	sleeper := &instantSleeper{}
	c, clk := newPacedController(t, pacer, sleeper)
	sleeper.clock = clk
	budget := NewBudget(clk.Now(), 30*time.Second)

	if err := c.WaitSend(context.Background(), budget, flow.SendControl); err != nil {
		t.Fatalf("first WaitSend: %v", err)
	}
	err := c.WaitFlood(context.Background(), budget, 15*time.Second)
	var slow *SlowDownError
	if !errors.As(err, &slow) {
		t.Fatalf("err = %v, want *SlowDownError once 20s of the budget is spent", err)
	}
}

func TestPacingCancellationNotSlowDown(t *testing.T) {
	t.Run("send cancelled during the wait returns the reservation", func(t *testing.T) {
		pacer := &fakePacer{delay: 10 * time.Second}
		sleeper := newBlockingSleeper()
		c, clk := newPacedController(t, pacer, sleeper)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.WaitSend(ctx, NewBudget(clk.Now(), 30*time.Second), flow.SendControl) }()
		sleeper.awaitStarted(t)
		cancel()
		err := receiveWithin(t, done)
		if !errors.Is(err, context.Canceled) || errors.Is(err, errs.ErrSlowDown) {
			t.Fatalf("err = %v, want context.Canceled only", err)
		}
		if _, cancelled := pacer.counts(); cancelled != 1 {
			t.Fatalf("cancelled = %d, want 1", cancelled)
		}
	})

	t.Run("flood wait cancelled during the wait", func(t *testing.T) {
		sleeper := newBlockingSleeper()
		c, clk := newPacedController(t, &fakePacer{}, sleeper)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.WaitFlood(ctx, NewBudget(clk.Now(), 30*time.Second), 10*time.Second) }()
		sleeper.awaitStarted(t)
		cancel()
		err := receiveWithin(t, done)
		if !errors.Is(err, context.Canceled) || errors.Is(err, errs.ErrSlowDown) {
			t.Fatalf("err = %v, want context.Canceled only", err)
		}
	})

	t.Run("cancelled before any reservation with an exhausted budget", func(t *testing.T) {
		pacer := &fakePacer{delay: time.Hour}
		c, clk := newPacedController(t, pacer, newBlockingSleeper())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		budget := NewBudget(clk.Now(), 0)
		if err := c.WaitSend(ctx, budget, flow.SendControl); !errors.Is(err, context.Canceled) || errors.Is(err, errs.ErrSlowDown) {
			t.Fatalf("WaitSend err = %v, want context.Canceled only", err)
		}
		if err := c.WaitFlood(ctx, budget, time.Hour); !errors.Is(err, context.Canceled) || errors.Is(err, errs.ErrSlowDown) {
			t.Fatalf("WaitFlood err = %v, want context.Canceled only", err)
		}
		if reserved, _ := pacer.counts(); reserved != 0 {
			t.Fatalf("reserved %d tokens for a cancelled context", reserved)
		}
	})
}

func TestNewRejectsNilPacer(t *testing.T) {
	_, err := New(Dependencies{
		Config: Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1},
		Clock:  clock.NewFake(testEpoch),
	})
	if !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("err = %v, want ErrMissingDependency", err)
	}
}

func TestSendLimiterSatisfiesPacer(t *testing.T) {
	var _ Pacer = (*flow.SendLimiter)(nil)
}
