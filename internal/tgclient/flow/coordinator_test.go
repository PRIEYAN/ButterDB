package flow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type scriptedSleeper struct {
	started chan time.Duration
	release chan struct{}
}

func newScriptedSleeper() *scriptedSleeper {
	return &scriptedSleeper{
		started: make(chan time.Duration, 8),
		release: make(chan struct{}, 8),
	}
}

func (s *scriptedSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	select {
	case s.started <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newTestCoordinator(t *testing.T, start time.Time) (*WriteCoordinator, *clock.Fake, *scriptedSleeper) {
	t.Helper()
	fake := clock.NewFake(start)
	sleeper := newScriptedSleeper()
	limiter, err := NewSendLimiterWithClock(fake, sleeper, 600, 100, 120, 20)
	if err != nil {
		t.Fatalf("NewSendLimiterWithClock: %v", err)
	}
	coordinator, err := NewWriteCoordinator(WriteCoordinatorDeps{Clock: fake, Sleeper: sleeper, Limiter: limiter})
	if err != nil {
		t.Fatalf("NewWriteCoordinator: %v", err)
	}
	return coordinator, fake, sleeper
}

func TestWriteCoordinatorRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		deps WriteCoordinatorDeps
	}{
		{"missing clock", WriteCoordinatorDeps{Sleeper: SystemSleeper{}, Limiter: NewSendLimiter(20, 5, 6, 1)}},
		{"missing sleeper", WriteCoordinatorDeps{Clock: clock.System{}, Limiter: NewSendLimiter(20, 5, 6, 1)}},
		{"missing limiter", WriteCoordinatorDeps{Clock: clock.System{}, Sleeper: SystemSleeper{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewWriteCoordinator(tt.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewWriteCoordinator error = %v, want missing dependency", err)
			}
		})
	}
}

func TestWriteCoordinatorSharesLongestFloodWaitAcrossWriteClasses(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	coordinator, fake, sleeper := newTestCoordinator(t, start)

	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)

	go func() {
		firstResult <- coordinator.Do(context.Background(), tgclient.WriteDocument, func() error {
			close(firstEntered)
			<-releaseFirst
			return newFloodWait(2 * time.Second)
		})
	}()
	go func() {
		secondResult <- coordinator.Do(context.Background(), tgclient.WriteControl, func() error {
			close(secondEntered)
			<-releaseSecond
			return newFloodWait(5 * time.Second)
		})
	}()

	<-firstEntered
	<-secondEntered
	close(releaseFirst)
	if err := <-firstResult; !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("document error = %v, want flood wait", err)
	}
	close(releaseSecond)
	if err := <-secondResult; !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("control error = %v, want flood wait", err)
	}

	deleteAction := make(chan struct{}, 1)
	documentAction := make(chan struct{}, 1)
	deleteResult := make(chan error, 1)
	documentResult := make(chan error, 1)
	go func() {
		deleteResult <- coordinator.Do(context.Background(), tgclient.WriteDelete, func() error {
			deleteAction <- struct{}{}
			return nil
		})
	}()
	go func() {
		documentResult <- coordinator.Do(context.Background(), tgclient.WriteDocument, func() error {
			documentAction <- struct{}{}
			return nil
		})
	}()

	for i := 0; i < 2; i++ {
		if wait := <-sleeper.started; wait != 5*time.Second {
			t.Fatalf("shared cooldown wait = %s, want 5s", wait)
		}
	}
	assertNoCoordinatorAction(t, deleteAction, "delete")
	assertNoCoordinatorAction(t, documentAction, "document")

	fake.Set(start.Add(5 * time.Second))
	sleeper.release <- struct{}{}
	sleeper.release <- struct{}{}
	<-deleteAction
	<-documentAction
	if err := <-deleteResult; err != nil {
		t.Fatalf("delete after cooldown: %v", err)
	}
	if err := <-documentResult; err != nil {
		t.Fatalf("document after cooldown: %v", err)
	}
}

func TestWriteCoordinatorSerializesMessagesAndRechecksCooldownAfterQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(1_700_000_000, 0)
		coordinator, fake, sleeper := newTestCoordinator(t, start)

		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstResult := make(chan error, 1)
		go func() {
			firstResult <- coordinator.Do(context.Background(), tgclient.WriteControl, func() error {
				close(firstEntered)
				<-releaseFirst
				return nil
			})
		}()
		<-firstEntered

		secondAction := make(chan struct{}, 1)
		secondResult := make(chan error, 1)
		go func() {
			secondResult <- coordinator.Do(context.Background(), tgclient.WriteChannel, func() error {
				secondAction <- struct{}{}
				return nil
			})
		}()
		synctest.Wait()
		assertNoCoordinatorAction(t, secondAction, "second message")

		err := coordinator.Do(context.Background(), tgclient.WriteDocument, func() error {
			return newFloodWait(4 * time.Second)
		})
		if !errors.Is(err, tgclient.ErrFloodWait) {
			t.Fatalf("document error = %v, want flood wait", err)
		}

		close(releaseFirst)
		if err := <-firstResult; err != nil {
			t.Fatalf("first message: %v", err)
		}
		if wait := <-sleeper.started; wait != 4*time.Second {
			t.Fatalf("queued message cooldown = %s, want 4s", wait)
		}
		assertNoCoordinatorAction(t, secondAction, "second message during cooldown")

		fake.Set(start.Add(4 * time.Second))
		sleeper.release <- struct{}{}
		<-secondAction
		if err := <-secondResult; err != nil {
			t.Fatalf("second message: %v", err)
		}
	})
}

func TestWriteCoordinatorCancellationPreventsAction(t *testing.T) {
	coordinator, _, _ := newTestCoordinator(t, time.Unix(1_700_000_000, 0))
	if err := coordinator.Do(context.Background(), tgclient.WriteDocument, func() error {
		return newFloodWait(time.Minute)
	}); !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("seed cooldown error = %v, want flood wait", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called atomic.Bool
	err := coordinator.Do(ctx, tgclient.WriteControl, func() error {
		called.Store(true)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call error = %v, want context canceled", err)
	}
	if called.Load() {
		t.Fatal("action ran after cancellation")
	}

	err = coordinator.Do(nil, tgclient.WriteControl, func() error {
		called.Store(true)
		return nil
	})
	if !errors.Is(err, ErrInvalidWriteRequest) {
		t.Fatalf("nil context error = %v, want invalid write request", err)
	}
	if called.Load() {
		t.Fatal("action ran with a nil context")
	}
}

func TestWriteCoordinatorRejectsUnknownClassAndNilAction(t *testing.T) {
	t.Parallel()

	coordinator, _, _ := newTestCoordinator(t, time.Unix(1_700_000_000, 0))
	if err := coordinator.Do(context.Background(), tgclient.WriteClass(99), func() error { return nil }); !errors.Is(err, ErrInvalidWriteRequest) {
		t.Fatalf("unknown class error = %v, want invalid write request", err)
	}
	if err := coordinator.Do(context.Background(), tgclient.WriteControl, nil); !errors.Is(err, ErrInvalidWriteRequest) {
		t.Fatalf("nil action error = %v, want invalid write request", err)
	}
}

func TestWriteCoordinatorConvertsGotdFloodWaitAndRecordsIt(t *testing.T) {
	t.Parallel()

	coordinator, fake, _ := newTestCoordinator(t, time.Unix(1_700_000_000, 0))
	err := coordinator.Do(context.Background(), tgclient.WriteControl, func() error {
		return rawPremiumFloodWait()
	})
	var typed *tgclient.FloodWaitError
	if !errors.As(err, &typed) || typed.Duration != 9*time.Second {
		t.Fatalf("Do error = %v, want *FloodWaitError of 9s", err)
	}
	coordinator.mu.Lock()
	blockedUntil := coordinator.blockedUntil
	coordinator.mu.Unlock()
	if want := fake.Now().Add(9 * time.Second); !blockedUntil.Equal(want) {
		t.Fatalf("blockedUntil = %v, want %v", blockedUntil, want)
	}
}

func TestWriteCoordinatorSatisfiesWriteGate(t *testing.T) {
	t.Parallel()

	var _ tgclient.WriteGate = (*WriteCoordinator)(nil)
	var _ tgclient.GetFileGate = (*GetFileLimiter)(nil)
}

func assertNoCoordinatorAction(t *testing.T, action <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-action:
		t.Fatalf("%s action ran before the shared cooldown elapsed", label)
	default:
	}
}

func TestWriteCoordinatorPacesEachClassWithItsOwnBucket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := clock.NewFake(time.Unix(1_700_000_000, 0))
		sleeper := &advancingSleeper{clock: fake}
		limiter, err := NewSendLimiterWithClock(fake, sleeper, 20, 1, 6, 1)
		if err != nil {
			t.Fatalf("NewSendLimiterWithClock: %v", err)
		}
		coordinator, err := NewWriteCoordinator(WriteCoordinatorDeps{Clock: fake, Sleeper: sleeper, Limiter: limiter})
		if err != nil {
			t.Fatalf("NewWriteCoordinator: %v", err)
		}
		noop := func() error { return nil }
		sends := []struct {
			class tgclient.WriteClass
			want  time.Duration
		}{
			{tgclient.WriteControl, 0},
			{tgclient.WriteDocument, 0},
			{tgclient.WriteDelete, 3 * time.Second},
			{tgclient.WriteDocument, 7 * time.Second},
		}
		for i, send := range sends {
			before := sleeper.total()
			if err := coordinator.Do(context.Background(), send.class, noop); err != nil {
				t.Fatalf("send %d: %v", i, err)
			}
			if got := sleeper.total() - before; got+time.Millisecond < send.want {
				t.Fatalf("send %d waited %s, want at least %s", i, got, send.want)
			}
		}
	})
}

func TestWriteCoordinatorFloodWaitStaysAFloorOnTopOfTheLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(1_700_000_000, 0)
		fake := clock.NewFake(start)
		sleeper := &advancingSleeper{clock: fake}
		limiter, err := NewSendLimiterWithClock(fake, sleeper, 20, 5, 6, 1)
		if err != nil {
			t.Fatalf("NewSendLimiterWithClock: %v", err)
		}
		coordinator, err := NewWriteCoordinator(WriteCoordinatorDeps{Clock: fake, Sleeper: sleeper, Limiter: limiter})
		if err != nil {
			t.Fatalf("NewWriteCoordinator: %v", err)
		}
		if err := coordinator.Do(context.Background(), tgclient.WriteControl, func() error {
			return newFloodWait(40 * time.Second)
		}); !errors.Is(err, tgclient.ErrFloodWait) {
			t.Fatalf("seed error = %v, want flood wait", err)
		}
		if err := coordinator.Do(context.Background(), tgclient.WriteControl, func() error { return nil }); err != nil {
			t.Fatalf("send after flood wait: %v", err)
		}
		if elapsed := fake.Now().Sub(start); elapsed < 40*time.Second {
			t.Fatalf("send ran after %s, want at least the 40s flood wait", elapsed)
		}
	})
}
