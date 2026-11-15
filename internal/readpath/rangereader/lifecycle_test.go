package rangereader

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

func TestNewRangeReaderRejectsMissingDependencies(t *testing.T) {
	fake := newStrictRangeFake(testBytes(16))
	full := Dependencies{
		Client:   fake,
		Resolver: fake,
		Clock:    clock.System{},
		Sleeper:  immediateSleeper,
		Random:   zeroReader{},
		Logger:   slog.New(slog.DiscardHandler),
	}
	cases := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"nil client", func(d *Dependencies) { d.Client = nil }},
		{"nil resolver", func(d *Dependencies) { d.Resolver = nil }},
		{"nil clock", func(d *Dependencies) { d.Clock = nil }},
		{"nil sleeper", func(d *Dependencies) { d.Sleeper = nil }},
		{"nil random", func(d *Dependencies) { d.Random = nil }},
		{"nil logger", func(d *Dependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := full
			tc.mutate(&deps)
			if _, err := NewRangeReader(deps, RangeReaderConfig{}); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("err = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := NewRangeReader(full, RangeReaderConfig{}); err != nil {
		t.Fatalf("complete dependencies rejected: %v", err)
	}
}

func TestNewRangeReaderRejectsInvalidRetryPolicy(t *testing.T) {
	fake := newStrictRangeFake(testBytes(16))
	cases := []struct {
		name   string
		policy RetryPolicy
	}{
		{"negative retries", RetryPolicy{MaxRetries: -1}},
		{"retries without wait limits", RetryPolicy{MaxRetries: 1}},
		{"transient retries without backoff", RetryPolicy{MaxTransientRetries: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRangeReader(Dependencies{
				Client:   fake,
				Resolver: fake,
				Clock:    clock.System{},
				Sleeper:  immediateSleeper,
				Random:   zeroReader{},
				Logger:   slog.New(slog.DiscardHandler),
			}, RangeReaderConfig{Retry: &tc.policy})
			if !errors.Is(err, ErrInvalidRetryPolicy) {
				t.Fatalf("err = %v, want ErrInvalidRetryPolicy", err)
			}
		})
	}
}

func TestReadBeforeStartAndAfterCloseIsUnavailable(t *testing.T) {
	fake := newStrictRangeFake(testBytes(1024))
	reader, err := NewRangeReader(Dependencies{
		Client:   fake,
		Resolver: fake,
		Clock:    clock.System{},
		Sleeper:  immediateSleeper,
		Random:   zeroReader{},
		Logger:   slog.New(slog.DiscardHandler),
	}, RangeReaderConfig{})
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if _, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 8), 0); !errors.Is(err, errs.ErrServiceUnavailable) {
		t.Fatalf("read before start err = %v, want ErrServiceUnavailable", err)
	}
	if err := reader.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := reader.Start(context.Background()); !errors.Is(err, errs.ErrServiceUnavailable) {
		t.Fatalf("second Start err = %v, want ErrServiceUnavailable", err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 8), 0); !errors.Is(err, errs.ErrServiceUnavailable) {
		t.Fatalf("read after close err = %v, want ErrServiceUnavailable", err)
	}
}

func TestCloseCancelsInFlightFetchesAndWaits(t *testing.T) {
	block := int64(blockBytes)
	fake := newGatedRangeFake(testBytes(int(block) * 2))
	reader, err := NewRangeReader(Dependencies{
		Client:   fake,
		Resolver: fake,
		Clock:    clock.System{},
		Sleeper:  immediateSleeper,
		Random:   zeroReader{},
		Logger:   slog.New(slog.DiscardHandler),
	}, RangeReaderConfig{})
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if err := reader.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ref := fake.ref()
	reader.prefetchBlock(ref, block)
	fake.awaitEntered(t, block)

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reader.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := fake.awaitOutcome(t, block); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight fetch ended with %v, want cancellation", err)
	}
}

func TestStartContextCancellationStopsFetches(t *testing.T) {
	block := int64(blockBytes)
	fake := newGatedRangeFake(testBytes(int(block) * 2))
	reader, err := NewRangeReader(Dependencies{
		Client:   fake,
		Resolver: fake,
		Clock:    clock.System{},
		Sleeper:  immediateSleeper,
		Random:   zeroReader{},
		Logger:   slog.New(slog.DiscardHandler),
	}, RangeReaderConfig{})
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	startCtx, stop := context.WithCancel(context.Background())
	if err := reader.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	reader.prefetchBlock(fake.ref(), block)
	fake.awaitEntered(t, block)
	stop()
	if err := fake.awaitOutcome(t, block); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch ended with %v, want cancellation", err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
