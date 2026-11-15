package scan

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type failingHistory struct {
	HistoryReader
	calls int
	fail  func(context.Context, int) error
}

func (f *failingHistory) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	f.calls++
	if err := f.fail(ctx, f.calls); err != nil {
		return nil, err
	}
	return f.HistoryReader.GetHistory(ctx, peer, offsetID, limit)
}

func failingHistoryEnv(t *testing.T, fail func(context.Context, int) error) (*env, *failingHistory) {
	t.Helper()
	var failing *failingHistory
	e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		failing = &failingHistory{HistoryReader: base, fail: fail}
		return failing
	}))
	return e, failing
}

func TestHistoryReadTransportRetry(t *testing.T) {
	dropped := fmt.Errorf("rpcDoRequest: retryUntilAck: engine forcibly closed: %w", context.Canceled)
	rejected := errors.New("CHANNEL_PRIVATE")
	for _, test := range []struct {
		name      string
		failures  int
		err       error
		wantCalls int
		wantErr   error
	}{
		{name: "reconnect", failures: 1, err: dropped, wantCalls: 2},
		{name: "bounded outage", failures: 100, err: dropped, wantCalls: 5, wantErr: dropped},
		{name: "API rejection", failures: 1, err: rejected, wantCalls: 1, wantErr: rejected},
		{name: "bare cancellation", failures: 1, err: context.Canceled, wantCalls: 1, wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, failing := failingHistoryEnv(t, func(_ context.Context, call int) error {
				if call <= test.failures {
					return test.err
				}
				return nil
			})
			_, err := e.scanner.getHistory(context.Background(), registryID, e.peer(registryID), 0)
			if !errors.Is(err, test.wantErr) || failing.calls != test.wantCalls {
				t.Fatalf("history error=%v calls=%d, want error=%v calls=%d", err, failing.calls, test.wantErr, test.wantCalls)
			}
		})
	}
}

func TestHistoryReadTransportBackoffDoublesToItsCap(t *testing.T) {
	dropped := fmt.Errorf("engine forcibly closed: %w", context.Canceled)
	e, _ := failingHistoryEnv(t, func(context.Context, int) error { return dropped })
	_, _ = e.scanner.getHistory(context.Background(), registryID, e.peer(registryID), 0)
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	got := e.sleeper.recorded()
	if len(got) != len(want) {
		t.Fatalf("backoffs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoffs = %v, want %v", got, want)
		}
	}
}

func TestHistoryReadFloodWaitSleepIsCapped(t *testing.T) {
	flood := &tgclient.FloodWaitError{Duration: 10 * time.Minute}
	e, failing := failingHistoryEnv(t, func(_ context.Context, call int) error {
		if call == 1 {
			return flood
		}
		return nil
	})
	if _, err := e.scanner.getHistory(context.Background(), registryID, e.peer(registryID), 0); err != nil {
		t.Fatalf("history read: %v", err)
	}
	if got := e.sleeper.recorded(); len(got) != 1 || got[0] != maxFloodWaitSleep || failing.calls != 2 {
		t.Fatalf("sleeps = %v calls = %d, want one sleep of %v", got, failing.calls, maxFloodWaitSleep)
	}
}

func TestHistoryReadCancellationDuringTransportBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, failing := failingHistoryEnv(t, func(context.Context, int) error {
		return fmt.Errorf("engine forcibly closed: %w", context.Canceled)
	})
	sleeping := make(chan struct{})
	e.sleeper.hook = func(ctx context.Context, _ time.Duration) error {
		close(sleeping)
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		<-sleeping
		cancel()
	}()
	_, err := e.scanner.getHistory(ctx, registryID, e.peer(registryID), 0)
	if !errors.Is(err, context.Canceled) || failing.calls != 1 {
		t.Fatalf("history error=%v calls=%d, want cancellation after one call", err, failing.calls)
	}
}

func TestHistoryReadMixedFailuresKeepIndependentBudgets(t *testing.T) {
	dropped := fmt.Errorf("engine forcibly closed: %w", context.Canceled)
	flood := &tgclient.FloodWaitError{}
	for _, test := range []struct {
		name      string
		failures  []error
		wantErr   error
		wantCalls int
	}{
		{name: "flood budget", failures: []error{flood, dropped, flood, dropped, flood, dropped, flood, dropped, flood, flood}, wantErr: tgclient.ErrFloodWait, wantCalls: 10},
		{name: "transport budget", failures: []error{dropped, flood, dropped, flood, dropped, flood, dropped, flood, dropped}, wantErr: dropped, wantCalls: 9},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, failing := failingHistoryEnv(t, func(_ context.Context, call int) error {
				if call <= len(test.failures) {
					return test.failures[call-1]
				}
				return nil
			})
			_, err := e.scanner.getHistory(context.Background(), registryID, e.peer(registryID), 0)
			if !errors.Is(err, test.wantErr) || failing.calls != test.wantCalls {
				t.Fatalf("history error=%v calls=%d, want error=%v calls=%d", err, failing.calls, test.wantErr, test.wantCalls)
			}
		})
	}
}

func TestAuthoritativeHistoryRecoversDroppedConnection(t *testing.T) {
	e, _ := failingHistoryEnv(t, func(_ context.Context, call int) error {
		if call == 3 {
			return fmt.Errorf("rpcDoRequest: retryUntilAck: engine forcibly closed: %w", context.Canceled)
		}
		return nil
	})
	messageID := e.seed(registryID, bucketCreateText(t, 1))

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("recover history: %v", err)
	}
	if _, ok := e.bucketPolicy(5001); !ok {
		t.Fatal("recovered bucket missing")
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != messageID {
		t.Fatalf("recovered authority=%v watermark=%d, want true/%d", state.InitialSyncDone, state.LastSyncedMsg, messageID)
	}
}
