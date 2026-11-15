package objectreader

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestOpenerCachesResolvedPeerAcrossRepeatedOpens(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("a"), 11: []byte("b")})
	resolver := newCountingPeerResolver(nil)
	deps := testDependencies(tg, unavailableKeys(nil))
	deps.Peers = resolver
	tg.peer = peerForChannel(testChannelID)
	opener := newTestOpener(t, deps)

	for _, msgID := range []int64{10, 11, 10} {
		reader := mustOpenReader(t, opener, plainMeta(msgID, []byte("a")))
		_ = reader.Close()
	}

	if got := resolver.callCount(testChannelID); got != 1 {
		t.Fatalf("ResolvePeer calls = %d, want 1", got)
	}
}

func TestOpenerCoalescesConcurrentPeerResolution(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	resolver := newCountingPeerResolver(func(ctx context.Context, channelID int64, _ int) (tgclient.InputPeer, error) {
		close(started)
		select {
		case <-release:
			return peerForChannel(channelID), nil
		case <-ctx.Done():
			return tgclient.InputPeer{}, ctx.Err()
		}
	})
	source := newPeerTestSource(t, resolver)

	const callers = 8
	results := make(chan peerResolveResult, callers)
	for range callers {
		go func() {
			peer, err := source.resolvePeer(context.Background(), testChannelID)
			results <- peerResolveResult{peer: peer, err: err}
		}()
	}
	waitForSignal(t, started, "peer resolution")
	waitForPeerWaiters(t, source, testChannelID, callers)
	close(release)

	for range callers {
		result := waitForPeerResult(t, results)
		if result.err != nil {
			t.Fatalf("resolvePeer: %v", result.err)
		}
		if want := peerForChannel(testChannelID); result.peer != want {
			t.Fatalf("resolvePeer = %+v, want %+v", result.peer, want)
		}
	}
	if got := resolver.callCount(testChannelID); got != 1 {
		t.Fatalf("ResolvePeer calls = %d, want 1", got)
	}
}

func TestOpenerCanceledPeerWaiterDoesNotPoisonSharedResolution(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	resolver := newCountingPeerResolver(func(ctx context.Context, channelID int64, _ int) (tgclient.InputPeer, error) {
		close(started)
		select {
		case <-release:
			return peerForChannel(channelID), nil
		case <-ctx.Done():
			return tgclient.InputPeer{}, ctx.Err()
		}
	})
	source := newPeerTestSource(t, resolver)

	canceledCtx, cancel := context.WithCancel(context.Background())
	canceledResult := make(chan peerResolveResult, 1)
	go func() {
		peer, err := source.resolvePeer(canceledCtx, testChannelID)
		canceledResult <- peerResolveResult{peer: peer, err: err}
	}()
	waitForSignal(t, started, "peer resolution")

	sharedResult := make(chan peerResolveResult, 1)
	go func() {
		peer, err := source.resolvePeer(context.Background(), testChannelID)
		sharedResult <- peerResolveResult{peer: peer, err: err}
	}()
	waitForPeerWaiters(t, source, testChannelID, 2)
	cancel()
	if result := waitForPeerResult(t, canceledResult); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled resolvePeer error = %v, want context.Canceled", result.err)
	}

	close(release)
	if result := waitForPeerResult(t, sharedResult); result.err != nil {
		t.Fatalf("shared resolvePeer: %v", result.err)
	}
	if _, err := source.resolvePeer(context.Background(), testChannelID); err != nil {
		t.Fatalf("cached resolvePeer: %v", err)
	}
	if got := resolver.callCount(testChannelID); got != 1 {
		t.Fatalf("ResolvePeer calls = %d, want 1", got)
	}
}

func TestOpenerDoesNotCachePeerErrorsOrAbandonedResolutions(t *testing.T) {
	t.Run("resolver error", func(t *testing.T) {
		sentinel := errors.New("temporary resolve failure")
		resolver := newCountingPeerResolver(func(_ context.Context, channelID int64, call int) (tgclient.InputPeer, error) {
			if call == 1 {
				return tgclient.InputPeer{}, sentinel
			}
			return peerForChannel(channelID), nil
		})
		source := newPeerTestSource(t, resolver)

		if _, err := source.resolvePeer(context.Background(), testChannelID); !errors.Is(err, sentinel) {
			t.Fatalf("first resolvePeer error = %v, want sentinel", err)
		}
		if _, err := source.resolvePeer(context.Background(), testChannelID); err != nil {
			t.Fatalf("retry resolvePeer: %v", err)
		}
		if got := resolver.callCount(testChannelID); got != 2 {
			t.Fatalf("ResolvePeer calls = %d, want 2", got)
		}
	})

	t.Run("last waiter cancels", func(t *testing.T) {
		firstCanceled := make(chan struct{})
		firstStarted := make(chan struct{})
		resolver := newCountingPeerResolver(func(ctx context.Context, channelID int64, call int) (tgclient.InputPeer, error) {
			if call == 1 {
				close(firstStarted)
				<-ctx.Done()
				close(firstCanceled)
				return tgclient.InputPeer{}, ctx.Err()
			}
			return peerForChannel(channelID), nil
		})
		source := newPeerTestSource(t, resolver)

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan peerResolveResult, 1)
		go func() {
			peer, err := source.resolvePeer(ctx, testChannelID)
			result <- peerResolveResult{peer: peer, err: err}
		}()
		waitForSignal(t, firstStarted, "first peer resolution")
		cancel()
		if got := waitForPeerResult(t, result); !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled resolvePeer error = %v, want context.Canceled", got.err)
		}
		waitForSignal(t, firstCanceled, "abandoned peer resolver cancellation")

		if _, err := source.resolvePeer(context.Background(), testChannelID); err != nil {
			t.Fatalf("retry resolvePeer: %v", err)
		}
		if got := resolver.callCount(testChannelID); got != 2 {
			t.Fatalf("ResolvePeer calls = %d, want 2", got)
		}
	})
}

func TestOpenerPeerCacheIsBoundedAndUsesLRUEviction(t *testing.T) {
	resolver := newCountingPeerResolver(nil)
	source := newPeerTestSource(t, resolver)

	for channelID := int64(1); channelID <= maxCachedPeers; channelID++ {
		if _, err := source.resolvePeer(context.Background(), channelID); err != nil {
			t.Fatalf("prime channel %d: %v", channelID, err)
		}
	}
	if _, err := source.resolvePeer(context.Background(), 1); err != nil {
		t.Fatalf("refresh channel 1: %v", err)
	}
	if _, err := source.resolvePeer(context.Background(), maxCachedPeers+1); err != nil {
		t.Fatalf("add overflow channel: %v", err)
	}
	if _, err := source.resolvePeer(context.Background(), 2); err != nil {
		t.Fatalf("resolve evicted channel 2: %v", err)
	}

	if got := resolver.callCount(1); got != 1 {
		t.Fatalf("refreshed channel calls = %d, want 1", got)
	}
	if got := resolver.callCount(2); got != 2 {
		t.Fatalf("evicted channel calls = %d, want 2", got)
	}
	if got := source.peerCache.Len(); got != maxCachedPeers {
		t.Fatalf("cached peers = %d, want %d", got, maxCachedPeers)
	}
}

func TestOpenerCloseCancelsPeerResolutionAndClearsCache(t *testing.T) {
	blockedChannel := int64(2)
	started := make(chan struct{})
	canceled := make(chan struct{})
	resolver := newCountingPeerResolver(func(ctx context.Context, channelID int64, _ int) (tgclient.InputPeer, error) {
		if channelID != blockedChannel {
			return peerForChannel(channelID), nil
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		return tgclient.InputPeer{}, ctx.Err()
	})
	source := newPeerTestSource(t, resolver)
	if _, err := source.resolvePeer(context.Background(), 1); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	result := make(chan peerResolveResult, 1)
	go func() {
		peer, err := source.resolvePeer(context.Background(), blockedChannel)
		result <- peerResolveResult{peer: peer, err: err}
	}()
	waitForSignal(t, started, "blocked peer resolution")
	closeCtx, cancel := closeContext()
	defer cancel()
	if err := source.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForSignal(t, canceled, "peer resolver cancellation")
	if got := waitForPeerResult(t, result); !errors.Is(got.err, ErrClosed) {
		t.Fatalf("resolvePeer after Close error = %v, want ErrClosed", got.err)
	}
	if got := source.peerCache.Len(); got != 0 {
		t.Fatalf("cached peers after Close = %d, want 0", got)
	}
}

type peerResolveResult struct {
	peer tgclient.InputPeer
	err  error
}

type countingPeerResolver struct {
	mu      sync.Mutex
	calls   map[int64]int
	resolve func(context.Context, int64, int) (tgclient.InputPeer, error)
}

func newCountingPeerResolver(resolve func(context.Context, int64, int) (tgclient.InputPeer, error)) *countingPeerResolver {
	if resolve == nil {
		resolve = func(_ context.Context, channelID int64, _ int) (tgclient.InputPeer, error) {
			return peerForChannel(channelID), nil
		}
	}
	return &countingPeerResolver{calls: make(map[int64]int), resolve: resolve}
}

func (r *countingPeerResolver) ResolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	r.mu.Lock()
	r.calls[channelID]++
	call := r.calls[channelID]
	resolve := r.resolve
	r.mu.Unlock()
	return resolve(ctx, channelID, call)
}

func (r *countingPeerResolver) callCount(channelID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[channelID]
}

func newPeerTestSource(t *testing.T, resolver PeerResolver) *StoredSource {
	t.Helper()
	return newTestSource(t, SourceDependencies{
		Peers:     resolver,
		Documents: newCountingDocumentResolver(nil),
		Stored:    emptyStored{},
		Clock:     clock.NewFake(time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)),
	})
}

func waitForPeerResult(t *testing.T, results <-chan peerResolveResult) peerResolveResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(resolverEventTimeout):
		t.Fatal("timed out waiting for peer resolution")
		return peerResolveResult{}
	}
}

func waitForPeerWaiters(t *testing.T, source *StoredSource, channelID int64, want int) {
	t.Helper()
	deadline := time.After(resolverEventTimeout)
	for {
		got := source.peerResolutions.Waiters(channelID)
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("peer resolution waiters = %d, want %d", got, want)
		default:
			runtime.Gosched()
		}
	}
}
