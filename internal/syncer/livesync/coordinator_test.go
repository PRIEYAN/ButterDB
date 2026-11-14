package livesync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
)

const (
	testDebounce = 100 * time.Millisecond
	testPoll     = time.Millisecond
	unknownProbe = int64(999999)
)

type fakeSignals struct {
	ch chan int64
}

func newFakeSignals() *fakeSignals {
	return &fakeSignals{ch: make(chan int64, 64)}
}

func (f *fakeSignals) Signals() <-chan int64 { return f.ch }

func (f *fakeSignals) send(id int64) { f.ch <- id }

type fakeSyncer struct {
	mu      sync.Mutex
	calls   []int64
	err     error
	onCall  func(ctx context.Context, channelID int64) error
	entered chan int64
}

func (f *fakeSyncer) Sync(ctx context.Context, channelID int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, channelID)
	err := f.err
	onCall := f.onCall
	entered := f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- channelID
	}
	if onCall != nil {
		return onCall(ctx, channelID)
	}
	return err
}

func (f *fakeSyncer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSyncer) snapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type fakeLister struct {
	mu       sync.Mutex
	channels []syncer.ChannelState
	err      error
	calls    int
}

func (f *fakeLister) Channels(context.Context) ([]syncer.ChannelState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.channels), nil
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLister) set(channels ...syncer.ChannelState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = channels
}

func live(ids ...int64) []syncer.ChannelState {
	out := make([]syncer.ChannelState, 0, len(ids))
	for _, id := range ids {
		out = append(out, syncer.ChannelState{ChannelID: id, Kind: syncer.KindBucket})
	}
	return out
}

type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logRecorder) contains(text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Contains(l.buf.Bytes(), []byte(text))
}

type harness struct {
	signals *fakeSignals
	syncer  *fakeSyncer
	lister  *fakeLister
	clock   *clock.Fake
	logs    *logRecorder
	coord   *Coordinator
}

func newHarness(t *testing.T, cfg Config, known ...int64) *harness {
	t.Helper()
	h := &harness{
		signals: newFakeSignals(),
		syncer:  &fakeSyncer{},
		lister:  &fakeLister{channels: live(known...)},
		clock:   clock.NewFake(time.Unix(1763000000, 0).UTC()),
		logs:    &logRecorder{},
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = testDebounce
	}
	if cfg.BackstopInterval == 0 {
		cfg.BackstopInterval = time.Hour
	}
	cfg.PollInterval = testPoll
	coord, err := NewCoordinator(Dependencies{
		Signals:  h.signals,
		Syncer:   h.syncer,
		Channels: h.lister,
		Clock:    h.clock,
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, cfg)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	h.coord = coord
	return h
}

func (h *harness) start(t *testing.T) {
	t.Helper()
	if err := h.coord.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.coord.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func (h *harness) settle(t *testing.T) {
	t.Helper()
	before := h.lister.callCount()
	h.signals.send(unknownProbe)
	eventually(t, func() bool { return h.lister.callCount() > before })
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func TestNewCoordinatorRejectsNilDependencies(t *testing.T) {
	full := Dependencies{
		Signals:  newFakeSignals(),
		Syncer:   &fakeSyncer{},
		Channels: &fakeLister{},
		Clock:    clock.NewFake(time.Time{}),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, tc := range []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{"nil signals", func(d *Dependencies) { d.Signals = nil }},
		{"nil syncer", func(d *Dependencies) { d.Syncer = nil }},
		{"nil channels", func(d *Dependencies) { d.Channels = nil }},
		{"nil clock", func(d *Dependencies) { d.Clock = nil }},
		{"nil logger", func(d *Dependencies) { d.Logger = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := full
			tc.mutate(&deps)
			coord, err := NewCoordinator(deps, Config{})
			if !errors.Is(err, errs.ErrMissingDependency) || coord != nil {
				t.Fatalf("NewCoordinator = %v, %v, want ErrMissingDependency", coord, err)
			}
		})
	}
}

func TestWithDefaultsUsesSpecifiedConstants(t *testing.T) {
	got := withDefaults(Config{BackstopInterval: -time.Second})
	want := Config{
		Debounce:         750 * time.Millisecond,
		BackstopInterval: 5 * time.Minute,
		SyncTimeout:      2 * time.Minute,
		PollInterval:     DefaultPollInterval,
	}
	if got != want {
		t.Fatalf("withDefaults = %+v, want %+v", got, want)
	}
}

func TestCoordinatorCoalescesSignalBursts(t *testing.T) {
	h := newHarness(t, Config{}, 42)
	h.start(t)

	h.signals.send(42)
	h.signals.send(42)
	h.signals.send(42)
	h.settle(t)
	h.clock.Advance(testDebounce)

	eventually(t, func() bool { return h.syncer.count() >= 1 })
	h.settle(t)
	h.clock.Advance(testDebounce)
	h.settle(t)
	if got := h.syncer.snapshot(); !slices.Equal(got, []int64{42}) {
		t.Fatalf("sync calls = %v, want [42]", got)
	}
}

func TestCoordinatorWaitsForDebounce(t *testing.T) {
	h := newHarness(t, Config{}, 42)
	h.start(t)

	h.signals.send(42)
	h.settle(t)
	h.clock.Advance(testDebounce - time.Millisecond)
	h.settle(t)
	if got := h.syncer.count(); got != 0 {
		t.Fatalf("sync calls before debounce = %d, want 0", got)
	}
	h.clock.Advance(time.Millisecond)
	eventually(t, func() bool { return h.syncer.count() == 1 })
}

func TestCoordinatorIgnoresUnknownChannels(t *testing.T) {
	h := newHarness(t, Config{}, 1)
	h.start(t)
	eventually(t, func() bool { return h.lister.callCount() == 1 })

	h.signals.send(99)
	h.settle(t)
	if got := h.lister.callCount(); got != 3 {
		t.Fatalf("known-channel loads = %d, want startup plus one reload per unknown signal", got)
	}
	h.clock.Advance(testDebounce)
	h.settle(t)
	if got := h.syncer.snapshot(); len(got) != 0 {
		t.Fatalf("sync calls = %v, want none for unknown channel 99", got)
	}

	h.signals.send(1)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, func() bool { return h.syncer.count() == 1 })
	if got := h.syncer.snapshot(); !slices.Equal(got, []int64{1}) {
		t.Fatalf("sync calls = %v, want [1] after a known-channel signal", got)
	}
}

func TestCoordinatorIgnoresDeletedAndNonPositiveChannels(t *testing.T) {
	h := newHarness(t, Config{})
	h.lister.set(
		syncer.ChannelState{ChannelID: 5, Deleted: true},
		syncer.ChannelState{ChannelID: -7},
		syncer.ChannelState{ChannelID: 6},
	)
	h.start(t)

	h.signals.send(5)
	h.signals.send(-7)
	h.signals.send(0)
	h.signals.send(6)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, func() bool { return h.syncer.count() == 1 })
	h.settle(t)
	if got := h.syncer.snapshot(); !slices.Equal(got, []int64{6}) {
		t.Fatalf("sync calls = %v, want [6]", got)
	}
}

func TestCoordinatorSyncsKnownChannelsSequentially(t *testing.T) {
	h := newHarness(t, Config{}, 3, 1, 2)
	var inFlight, peak atomic.Int32
	h.syncer.onCall = func(context.Context, int64) error {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	}
	h.start(t)

	h.signals.send(3)
	h.signals.send(1)
	h.signals.send(2)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, func() bool { return h.syncer.count() == 3 })
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent syncs = %d, want 1", got)
	}
	if got := h.syncer.snapshot(); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("sync order = %v, want [1 2 3]", got)
	}
}

func TestCoordinatorBackstopSyncsKnownChannels(t *testing.T) {
	h := newHarness(t, Config{BackstopInterval: 10 * time.Minute}, 7, 8)
	h.start(t)
	h.settle(t)

	h.clock.Advance(10 * time.Minute)
	eventually(t, func() bool { return h.syncer.count() == 2 })
	if got := h.syncer.snapshot(); !slices.Equal(got, []int64{7, 8}) {
		t.Fatalf("sync calls = %v, want [7 8]", got)
	}
}

func TestBackstopFiresUnderContinuousSignals(t *testing.T) {
	const backstop = 5 * time.Second
	h := newHarness(t, Config{Debounce: time.Second, BackstopInterval: backstop}, 1, 2)
	h.start(t)
	h.settle(t)

	step := 100 * time.Millisecond
	for elapsed := time.Duration(0); elapsed < 4500*time.Millisecond; elapsed += step {
		h.signals.send(1)
		h.settle(t)
		h.clock.Advance(step)
	}
	if got := h.syncer.count(); got != 0 {
		t.Fatalf("sync calls while signals keep resetting the debounce = %d, want 0", got)
	}

	for elapsed := time.Duration(0); elapsed < 1500*time.Millisecond; elapsed += step {
		h.signals.send(1)
		h.settle(t)
		h.clock.Advance(step)
	}
	eventually(t, func() bool { return slices.Contains(h.syncer.snapshot(), 2) })
}

func TestCoordinatorLogsFailureAndDoesNotRetry(t *testing.T) {
	h := newHarness(t, Config{}, 5)
	h.syncer.err = errors.New("boom")
	h.start(t)

	h.signals.send(5)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, func() bool { return h.logs.contains("livesync: sync failed") })

	h.clock.Advance(time.Minute)
	h.settle(t)
	h.settle(t)
	if got := h.syncer.count(); got != 1 {
		t.Fatalf("sync calls after failure = %d, want 1 with no immediate retry", got)
	}

	h.syncer.mu.Lock()
	h.syncer.err = nil
	h.syncer.mu.Unlock()
	h.signals.send(5)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, func() bool { return h.syncer.count() == 2 })
}

func TestCoordinatorBoundsEachSyncWithTimeout(t *testing.T) {
	h := newHarness(t, Config{SyncTimeout: 20 * time.Millisecond}, 4)
	var timedOut atomic.Bool
	h.syncer.onCall = func(ctx context.Context, _ int64) error {
		<-ctx.Done()
		timedOut.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
		return ctx.Err()
	}
	h.start(t)

	h.signals.send(4)
	h.settle(t)
	h.clock.Advance(testDebounce)
	eventually(t, timedOut.Load)
	eventually(t, func() bool { return h.logs.contains("livesync: sync failed") })
}

func TestCoordinatorKeepsKnownSetWhenListingFails(t *testing.T) {
	h := newHarness(t, Config{BackstopInterval: time.Minute}, 9)
	h.start(t)
	h.settle(t)

	h.lister.mu.Lock()
	h.lister.err = errors.New("db down")
	h.lister.mu.Unlock()
	h.clock.Advance(time.Minute)
	eventually(t, func() bool { return h.syncer.count() == 1 })
	if !h.logs.contains("livesync: list channels failed") {
		t.Fatal("list failure was not logged")
	}
}

func TestCoordinatorStopsWhenSignalSourceCloses(t *testing.T) {
	h := newHarness(t, Config{})
	h.start(t)
	close(h.signals.ch)

	h.coord.mu.Lock()
	done := h.coord.done
	h.coord.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit after the signal source closed")
	}
}

func TestCloseJoinsGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	h := newHarness(t, Config{}, 11)
	entered := make(chan int64, 1)
	var returned atomic.Bool
	h.syncer.entered = entered
	h.syncer.onCall = func(ctx context.Context, _ int64) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		returned.Store(true)
		return ctx.Err()
	}
	if err := h.coord.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.signals.send(11)
	h.settle(t)
	h.clock.Advance(testDebounce)
	<-entered

	if err := h.coord.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !returned.Load() {
		t.Fatal("Close returned before the in-flight sync returned")
	}
	eventually(t, func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestCloseHonoursItsContext(t *testing.T) {
	h := newHarness(t, Config{}, 12)
	entered := make(chan int64, 1)
	release := make(chan struct{})
	h.syncer.entered = entered
	h.syncer.onCall = func(context.Context, int64) error {
		<-release
		return nil
	}
	if err := h.coord.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.signals.send(12)
	h.settle(t)
	h.clock.Advance(testDebounce)
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.coord.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline exceeded", err)
	}
	close(release)
}

func TestStartAndCloseLifecycle(t *testing.T) {
	h := newHarness(t, Config{}, 1)
	if err := h.coord.Close(context.Background()); err != nil {
		t.Fatalf("Close before Start = %v, want nil", err)
	}
	for range 2 {
		if err := h.coord.Start(context.Background()); err != nil {
			t.Fatalf("Start = %v", err)
		}
		if err := h.coord.Start(context.Background()); err != nil {
			t.Fatalf("second Start = %v, want idempotent nil", err)
		}
		if err := h.coord.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if err := h.coord.Close(context.Background()); err != nil {
			t.Fatalf("second Close = %v, want idempotent nil", err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.coord.Start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with canceled context = %v, want context.Canceled", err)
	}
}
