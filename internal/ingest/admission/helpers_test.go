package admission

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var testEpoch = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

const guardTimeout = 5 * time.Second

type instantSleeper struct {
	mu    sync.Mutex
	clock *clock.Fake
	waits []time.Duration
}

func (s *instantSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.waits = append(s.waits, wait)
	s.mu.Unlock()
	s.clock.Advance(wait)
	return nil
}

func (s *instantSleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

type blockingSleeper struct {
	started chan time.Duration
	fire    chan struct{}
}

func newBlockingSleeper() *blockingSleeper {
	return &blockingSleeper{started: make(chan time.Duration, 16), fire: make(chan struct{}, 16)}
}

func (s *blockingSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	s.started <- wait
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.fire:
		return nil
	}
}

func (s *blockingSleeper) awaitStarted(t *testing.T) time.Duration {
	t.Helper()
	select {
	case wait := <-s.started:
		return wait
	case <-time.After(guardTimeout):
		t.Fatal("sleeper was never entered")
		return 0
	}
}

func newTestController(t *testing.T, cfg Config, sleeper Sleeper) (*Controller, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(testEpoch)
	c, err := New(Dependencies{Config: cfg, Pacer: noDelayPacer{}, Clock: clk, Sleeper: sleeper})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, clk
}

func receiveWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(guardTimeout):
		t.Fatal("timed out waiting for channel")
		var zero T
		return zero
	}
}

type noDelayPacer struct{}

func (noDelayPacer) Reserve(flow.SendClass) (time.Duration, func()) {
	return 0, func() {}
}
