package scan

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

type blockedHistory struct {
	HistoryReader
	blockedChannel int64
	entered        chan struct{}
	release        chan struct{}
	once           sync.Once
	calls          atomic.Int64
}

func (b *blockedHistory) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	if peer.ChannelID == b.blockedChannel {
		b.calls.Add(1)
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.HistoryReader.GetHistory(ctx, peer, offsetID, limit)
}

func TestChannelAdmissionHonorsCancellation(t *testing.T) {
	for _, name := range []string{"authority", "initial", "full scan"} {
		t.Run(name, func(t *testing.T) {
			blocked := &blockedHistory{blockedChannel: registryID, entered: make(chan struct{}), release: make(chan struct{})}
			e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
				blocked.HistoryReader = base
				return blocked
			}))
			e.addChannel(bucketID, syncer.KindBucket)
			release := sync.OnceFunc(func() { close(blocked.release) })

			holder := make(chan struct{})
			var holderErr error
			go func() {
				defer close(holder)
				releaseGate, err := e.gate.Acquire(t.Context(), registryID)
				if err != nil {
					holderErr = err
					return
				}
				defer releaseGate()
				holderErr = e.scanner.EnsureAuthoritative(t.Context(), registryID)
			}()
			t.Cleanup(func() {
				release()
				select {
				case <-holder:
				case <-time.After(5 * time.Second):
					t.Error("holding scan did not finish")
				}
			})
			select {
			case <-blocked.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("holding scan did not reach Telegram")
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				releaseGate, err := e.gate.Acquire(ctx, registryID)
				if err != nil {
					result <- err
					return
				}
				defer releaseGate()
				result <- e.runScanOperation(ctx, name)
			}()
			<-started
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("waiting operation: %v, want context cancellation", err)
				}
			case <-time.After(time.Second):
				release()
				t.Fatal("canceled operation waited for the holding scan")
			}
			if calls := blocked.calls.Load(); calls != 1 {
				t.Fatalf("same-channel history reads = %d, want only the holding scan", calls)
			}

			otherCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			otherRelease, err := e.gate.Acquire(otherCtx, bucketID)
			if err != nil {
				t.Fatalf("independent channel admission: %v", err)
			}
			err = e.scanner.EnsureAuthoritative(otherCtx, bucketID)
			otherRelease()
			if err != nil {
				t.Fatalf("independent channel scan: %v", err)
			}

			release()
			select {
			case <-holder:
				if holderErr != nil {
					t.Fatalf("canceling the waiter affected the holder: %v", holderErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("holding scan did not finish")
			}
			reuse, err := e.gate.Acquire(otherCtx, registryID)
			if err != nil {
				t.Fatalf("channel reuse after cancellation: %v", err)
			}
			reuse()
		})
	}
}

func (e *env) runScanOperation(ctx context.Context, name string) error {
	switch name {
	case "initial":
		return e.scanner.InitialSyncEmptyChannel(ctx, registryID)
	case "full scan":
		return e.scanner.FullScan(ctx, registryID)
	}
	return e.scanner.EnsureAuthoritative(ctx, registryID)
}

func TestGateReleaseIsIdempotent(t *testing.T) {
	gate := NewGate()
	release, err := gate.Acquire(context.Background(), registryID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	second, err := gate.Acquire(context.Background(), registryID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.Acquire(ctx, registryID); !errors.Is(err, context.Canceled) {
		t.Fatalf("second holder admitted after a double release: %v", err)
	}
	second()
}

func TestFloodWaitCancellationReleasesChannel(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.sleeper.hook = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if err := e.client.QueueFaults(faketg.MethodGetHistory, faketg.Fault{Kind: faketg.FaultFloodWait, Wait: time.Second}); err != nil {
		t.Fatal(err)
	}
	e.seed(registryID, bucketCreateText(t, 1))

	releaseGate, err := e.gate.Acquire(ctx, registryID)
	if err != nil {
		t.Fatal(err)
	}
	err = e.scanner.EnsureAuthoritative(ctx, registryID)
	releaseGate()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("flood-wait cancellation: %v", err)
	}
	if state := e.state(registryID); state.LastSyncedMsg != 0 || state.InitialSyncDone {
		t.Fatalf("canceled scan moved the cursor: %#v", state)
	}

	e.sleeper.hook = nil
	reacquired, err := e.gate.Acquire(t.Context(), registryID)
	if err != nil {
		t.Fatalf("channel stayed held after cancellation: %v", err)
	}
	defer reacquired()
	if err := e.scanner.EnsureAuthoritative(t.Context(), registryID); err != nil {
		t.Fatalf("scan after cancellation: %v", err)
	}
	if _, ok := e.bucketPolicy(5001); !ok {
		t.Fatal("retry did not project the pending operation")
	}
}
