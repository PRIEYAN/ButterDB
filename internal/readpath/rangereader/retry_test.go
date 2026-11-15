package rangereader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func testRetryPolicy() *RetryPolicy {
	policy := defaultRangeRetryPolicy()
	policy.TransientJitter = 0
	return &policy
}

func TestRangeReaderRetriesTransientTransportErrors(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.transientFailures = 2

	var sleeps int
	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		sleeps++
		return ctx.Err()
	}))

	buf := make([]byte, 64)
	n, err := reader.ReadStoredAt(context.Background(), fake.ref(), buf, 0)
	if err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if n != len(buf) || !bytes.Equal(buf, data[:len(buf)]) {
		t.Fatalf("read mismatch n=%d", n)
	}
	if calls := fake.calls(); len(calls) != 3 {
		t.Fatalf("calls = %+v, want initial + 2 transient retries", calls)
	}
	if sleeps != 2 {
		t.Fatalf("transient sleeps = %d, want 2", sleeps)
	}
}

func TestRangeReaderDoesNotRetryNonTransientErrors(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	wantErr := errors.New("telegram: document expired")
	fake.failErr = wantErr

	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(context.Context, time.Duration) error {
		t.Fatal("unexpected sleep for non-transient error")
		return nil
	}))

	_, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), 0)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want one non-retried attempt", calls)
	}
}

func TestRangeReaderRetriesTruncatedShortRead(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.shortReadFailures = 1

	var sleeps int
	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		sleeps++
		return ctx.Err()
	}))

	buf := make([]byte, 64)
	n, err := reader.ReadStoredAt(context.Background(), fake.ref(), buf, 0)
	if err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if n != len(buf) || !bytes.Equal(buf, data[:len(buf)]) {
		t.Fatalf("read mismatch n=%d", n)
	}
	if calls := fake.calls(); len(calls) != 2 {
		t.Fatalf("calls = %+v, want retry after truncated read", calls)
	}
	if sleeps != 1 {
		t.Fatalf("sleeps = %d, want one transient backoff", sleeps)
	}
}

func TestRangeReaderAcceptsFullBufferReadWithEOF(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.fullReadEOFFailures = 1

	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(context.Context, time.Duration) error {
		t.Fatal("unexpected retry after full-buffer read")
		return nil
	}))

	buf := make([]byte, 64)
	n, err := reader.ReadStoredAt(context.Background(), fake.ref(), buf, 0)
	if err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if n != len(buf) || !bytes.Equal(buf, data[:len(buf)]) {
		t.Fatalf("read mismatch n=%d", n)
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want no retry after full-buffer EOF", calls)
	}
}

func TestRangeReaderCancellationDuringTransientBackoff(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.transientFailures = 10

	ctx, cancel := context.WithCancel(context.Background())
	var sleeps int
	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(context.Context, time.Duration) error {
		sleeps++
		cancel()
		return ctx.Err()
	}))

	_, err := reader.ReadStoredAt(ctx, fake.ref(), make([]byte, 64), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sleeps != 1 {
		t.Fatalf("sleeps = %d, want cancellation during first backoff", sleeps)
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want no retry after cancellation", calls)
	}
}

func TestRangeReaderDoesNotRetryBeforeLongFloodWait(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.floodWaits = 1
	fake.floodWait = time.Hour

	var sleeps int
	policy := testRetryPolicy()
	policy.MaxWait = time.Millisecond
	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: policy}, SleeperFunc(func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}))

	_, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), 0)
	if !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("err = %v, want FLOOD_WAIT", err)
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want no early retry before long FLOOD_WAIT", calls)
	}
	if sleeps != 0 {
		t.Fatalf("sleeps = %d, want no capped/early sleep", sleeps)
	}
}

func TestRangeReaderReleasesConcurrencyBeforeTransientBackoff(t *testing.T) {
	data := testBytes(blockBytes * 2)
	fake := newStrictRangeFake(data)
	fake.transientFailuresByOffset = map[int64]int{0: 1}

	firstSleeping := make(chan struct{})
	releaseSleep := make(chan struct{})
	var sleepOnce sync.Once
	reader := startReaderSleeping(t, fake, RangeReaderConfig{Retry: testRetryPolicy()}, SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		sleepOnce.Do(func() { close(firstSleeping) })
		select {
		case <-releaseSleep:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))

	firstErr := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), 0)
		firstErr <- err
	}()

	select {
	case <-firstSleeping:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first transient backoff")
	}

	secondErr := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), int64(blockBytes))
		secondErr <- err
	}()

	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("second read while first backs off: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second read blocked behind transient backoff; concurrency slot was not released")
	}

	close(releaseSleep)
	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first read after transient retry: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first retry to finish")
	}
}

func TestStrictRangeFakeTransientErrorClassifiesAsRetryable(t *testing.T) {
	err := strictRangeTransientError()
	if !isTransientTransport(err) {
		t.Fatalf("test transient error %q is not classified as transient", err)
	}
}

func strictRangeTransientError() error {
	return fmt.Errorf("rpcDoRequest: retryUntilAck: engine forcibly closed: %w", context.Canceled)
}

var _ rangeClient = (*strictRangeFake)(nil)
