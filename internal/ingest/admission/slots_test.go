package admission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

func acquireAsync(c *Controller, ctx context.Context, b Budget, channel int64, key object.Key) <-chan acquireResult {
	out := make(chan acquireResult, 1)
	go func() {
		release, err := c.AcquirePut(ctx, b, channel, key)
		out <- acquireResult{release: release, err: err}
	}()
	return out
}

type acquireResult struct {
	release func()
	err     error
}

func TestSlotsFIFO(t *testing.T) {
	sleeper := newBlockingSleeper()
	c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, sleeper)
	budget := NewBudget(clk.Now(), 30*time.Second)

	holder, err := c.AcquirePut(context.Background(), budget, 1, "holder")
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	keys := []object.Key{"a", "b", "c", "d"}
	results := make([]<-chan acquireResult, len(keys))
	for i, key := range keys {
		results[i] = acquireAsync(c, context.Background(), budget, 1, key)
		sleeper.awaitStarted(t)
	}

	holder()
	for i, key := range keys {
		got := receiveWithin(t, results[i])
		if got.err != nil {
			t.Fatalf("waiter %q: %v", key, got.err)
		}
		for j := i + 1; j < len(keys); j++ {
			select {
			case <-results[j]:
				t.Fatalf("waiter %q was granted before waiter %q released", keys[j], key)
			default:
			}
		}
		got.release()
	}
}

func TestPerKeyLimit(t *testing.T) {
	tests := []struct {
		name         string
		sameChannel  bool
		secondKey    object.Key
		wantBlocking bool
	}{
		{"same key in same bucket blocks", true, "hot", true},
		{"same key name in other bucket proceeds", false, "hot", false},
		{"other key in same bucket proceeds", true, "cold", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sleeper := newBlockingSleeper()
			c, clk := newTestController(t, Config{MaxConcurrentPuts: 4, MaxConcurrentPutsPerKey: 1}, sleeper)
			budget := NewBudget(clk.Now(), 30*time.Second)

			first, err := c.AcquirePut(context.Background(), budget, 1, "hot")
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			channel := int64(1)
			if !tt.sameChannel {
				channel = 2
			}
			second := acquireAsync(c, context.Background(), budget, channel, tt.secondKey)
			if !tt.wantBlocking {
				got := receiveWithin(t, second)
				if got.err != nil {
					t.Fatalf("second acquire: %v", got.err)
				}
				got.release()
				first()
				return
			}
			sleeper.awaitStarted(t)
			select {
			case <-second:
				t.Fatal("second acquire for the same key did not wait")
			default:
			}
			other, err := c.AcquirePut(context.Background(), budget, 1, "cold")
			if err != nil {
				t.Fatalf("other key behind a hot key waiter: %v", err)
			}
			other()
			first()
			got := receiveWithin(t, second)
			if got.err != nil {
				t.Fatalf("second acquire after release: %v", got.err)
			}
			got.release()
		})
	}
}

func TestSlotReleaseOnPanicPath(t *testing.T) {
	c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, newBlockingSleeper())
	budget := NewBudget(clk.Now(), 30*time.Second)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate")
			}
		}()
		release, err := c.AcquirePut(context.Background(), budget, 1, "k")
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer release()
		panic("boom")
	}()

	release, err := c.AcquirePut(context.Background(), NewBudget(clk.Now(), 0), 1, "k")
	if err != nil {
		t.Fatalf("slot was not returned after panic: %v", err)
	}
	release()
	release()
	again, err := c.AcquirePut(context.Background(), NewBudget(clk.Now(), 0), 1, "k")
	if err != nil {
		t.Fatalf("slot unavailable after double release: %v", err)
	}
	if _, err := c.AcquirePut(context.Background(), NewBudget(clk.Now(), 0), 1, "k"); err == nil {
		t.Fatal("double release freed a second slot")
	}
	again()
}

func TestBudgetExhaustedReturnsSlowDown(t *testing.T) {
	t.Run("already exhausted budget does not wait", func(t *testing.T) {
		sleeper := newBlockingSleeper()
		c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, sleeper)
		held, err := c.AcquirePut(context.Background(), NewBudget(clk.Now(), 30*time.Second), 1, "a")
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer held()

		_, err = c.AcquirePut(context.Background(), NewBudget(clk.Now(), 0), 1, "b")
		var slow *SlowDownError
		if !errors.As(err, &slow) {
			t.Fatalf("err = %v, want *SlowDownError", err)
		}
		if slow.RetryAfter != time.Second {
			t.Fatalf("RetryAfter = %s, want 1s", slow.RetryAfter)
		}
		select {
		case <-sleeper.started:
			t.Fatal("exhausted budget still started a wait")
		default:
		}
	})

	t.Run("budget expires while queued", func(t *testing.T) {
		sleeper := newBlockingSleeper()
		c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, sleeper)
		budget := NewBudget(clk.Now(), 30*time.Second)
		held, err := c.AcquirePut(context.Background(), budget, 1, "a")
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}

		waiting := acquireAsync(c, context.Background(), budget, 1, "b")
		if wait := sleeper.awaitStarted(t); wait != 30*time.Second {
			t.Fatalf("budget wait = %s, want 30s", wait)
		}
		sleeper.fire <- struct{}{}
		got := receiveWithin(t, waiting)
		if !errors.As(got.err, new(*SlowDownError)) {
			t.Fatalf("err = %v, want *SlowDownError", got.err)
		}

		held()
		next, err := c.AcquirePut(context.Background(), budget, 1, "c")
		if err != nil {
			t.Fatalf("abandoned waiter kept a slot: %v", err)
		}
		next()
	})

	t.Run("slot granted before the budget wait is accepted", func(t *testing.T) {
		sleeper := newBlockingSleeper()
		c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, sleeper)
		budget := NewBudget(clk.Now(), 30*time.Second)
		held, err := c.AcquirePut(context.Background(), budget, 1, "a")
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		waiting := acquireAsync(c, context.Background(), budget, 1, "b")
		sleeper.awaitStarted(t)
		held()
		got := receiveWithin(t, waiting)
		if got.err != nil {
			t.Fatalf("waiter: %v", got.err)
		}
		got.release()
	})
}

func TestRetryAfterRoundedUpMinOne(t *testing.T) {
	tests := []struct {
		name string
		wait time.Duration
		want time.Duration
	}{
		{"zero is one second", 0, time.Second},
		{"negative is one second", -5 * time.Second, time.Second},
		{"one millisecond rounds to one second", time.Millisecond, time.Second},
		{"exactly one second", time.Second, time.Second},
		{"just over one second rounds to two", time.Second + time.Nanosecond, 2 * time.Second},
		{"one and a half rounds to two", 1500 * time.Millisecond, 2 * time.Second},
		{"whole seconds stay", 30 * time.Second, 30 * time.Second},
		{"minute and a bit", 61 * time.Second, 61 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newSlowDown("test", tt.wait)
			if got.RetryAfter != tt.want {
				t.Fatalf("RetryAfter = %s, want %s", got.RetryAfter, tt.want)
			}
		})
	}
}

func TestCancellationNotSlowDown(t *testing.T) {
	t.Run("cancelled while queued", func(t *testing.T) {
		sleeper := newBlockingSleeper()
		c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, sleeper)
		budget := NewBudget(clk.Now(), 30*time.Second)
		held, err := c.AcquirePut(context.Background(), budget, 1, "a")
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		waiting := acquireAsync(c, ctx, budget, 1, "b")
		sleeper.awaitStarted(t)
		cancel()
		got := receiveWithin(t, waiting)
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}
		if errors.Is(got.err, errs.ErrSlowDown) {
			t.Fatal("cancellation mapped to SlowDown")
		}
		held()
		next, err := c.AcquirePut(context.Background(), budget, 1, "c")
		if err != nil {
			t.Fatalf("cancelled waiter kept a slot: %v", err)
		}
		next()
	})

	t.Run("cancelled before acquiring with an exhausted budget", func(t *testing.T) {
		c, clk := newTestController(t, Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}, newBlockingSleeper())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.AcquirePut(ctx, NewBudget(clk.Now(), 0), 1, "a")
		if !errors.Is(err, context.Canceled) || errors.Is(err, errs.ErrSlowDown) {
			t.Fatalf("err = %v, want context.Canceled only", err)
		}
	})
}

func TestSlowDownUnwrapsToSentinel(t *testing.T) {
	err := error(newSlowDown("flood wait", 3*time.Second))
	wrapped := errors.Join(errors.New("outer"), err)
	if !errors.Is(wrapped, errs.ErrSlowDown) {
		t.Fatal("SlowDownError does not unwrap to errs.ErrSlowDown")
	}
	var slow *SlowDownError
	if !errors.As(wrapped, &slow) || slow.RetryAfter != 3*time.Second || slow.Reason != "flood wait" {
		t.Fatalf("errors.As lost fields: %+v", slow)
	}
	if got := errs.Code(err); got != errs.CodeSlowDown {
		t.Fatalf("code = %q, want %q", got, errs.CodeSlowDown)
	}
	if err.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestNewRejectsBadDependencies(t *testing.T) {
	tests := []struct {
		name string
		deps Dependencies
	}{
		{"nil clock", Dependencies{Config: Config{MaxConcurrentPuts: 1, MaxConcurrentPutsPerKey: 1}}},
		{"zero puts", Dependencies{Config: Config{MaxConcurrentPutsPerKey: 1}, Clock: clock.NewFake(testEpoch)}},
		{"zero puts per key", Dependencies{Config: Config{MaxConcurrentPuts: 1}, Clock: clock.NewFake(testEpoch)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.deps); err == nil {
				t.Fatal("New accepted bad dependencies")
			}
		})
	}
}
