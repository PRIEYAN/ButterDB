package objectreader

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func bodyMeta(msgID, size int64) object.ObjectMeta {
	return object.ObjectMeta{ChannelID: testChannelID, Key: "doc", PutMsgID: msgID + 1, BodyMsgID: msgID, StoredSize: size, PlaintextSize: size}
}

func TestDocumentRefCacheProductionDefaultsAreConservative(t *testing.T) {
	if maxCachedDocumentRefs < 256 || maxCachedDocumentRefs > 512 {
		t.Fatalf("document ref capacity = %d, want 256..512", maxCachedDocumentRefs)
	}
	if documentRefCacheTTL < 2*time.Minute || documentRefCacheTTL > 5*time.Minute {
		t.Fatalf("document ref TTL = %s, want 2m..5m", documentRefCacheTTL)
	}
}

func TestDocumentRefCacheIsTTLBoundedLRUAndUsesDefensiveCopies(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC))
	cache := newDocumentRefCache(2, time.Minute, fake)
	peer := peerForChannel(testChannelID)
	firstKey := documentRefKey{peer: peer, msgID: 1, storedSize: 2}
	secondKey := documentRefKey{peer: peer, msgID: 2, storedSize: 2}
	thirdKey := documentRefKey{peer: peer, msgID: 3, storedSize: 2}
	first := documentRefFor(firstKey, 1)

	cache.Put(firstKey, first)
	first.FileReference[0] = 9
	got, ok := cache.Get(firstKey)
	if !ok || got.FileReference[0] != 1 {
		t.Fatalf("cached ref after caller mutation = %+v, ok %v", got, ok)
	}
	got.FileReference[0] = 8
	again, ok := cache.Get(firstKey)
	if !ok || again.FileReference[0] != 1 {
		t.Fatalf("cached ref after result mutation = %+v, ok %v", again, ok)
	}

	cache.Put(secondKey, documentRefFor(secondKey, 2))
	if _, ok := cache.Get(firstKey); !ok {
		t.Fatal("refresh first key: cache miss")
	}
	cache.Put(thirdKey, documentRefFor(thirdKey, 3))
	if _, ok := cache.Get(secondKey); ok {
		t.Fatal("least-recently-used key remained cached")
	}
	if got := cache.Len(); got != 2 {
		t.Fatalf("cache length = %d, want 2", got)
	}

	fake.Advance(time.Minute)
	if _, ok := cache.Get(firstKey); ok {
		t.Fatal("first key remained cached at its expiry time")
	}
	if _, ok := cache.Get(thirdKey); ok {
		t.Fatal("third key remained cached at its expiry time")
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("cache length after expiry = %d, want 0", got)
	}
}

func TestDocumentResolutionCacheKeysAllProjectedIdentity(t *testing.T) {
	peer := peerForChannel(testChannelID)
	otherPeer := tgclient.InputPeer{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash + 1}
	resolvedSizes := []int64{1, 1, 2, 2}
	fake := newCountingDocumentResolver(func(_ context.Context, resolvedPeer tgclient.InputPeer, msgID int64, call int) (tgclient.DocumentRef, error) {
		return tgclient.DocumentRef{
			Peer:          resolvedPeer,
			MsgID:         msgID,
			Size:          resolvedSizes[call-1],
			FileReference: []byte{byte(call)},
		}, nil
	})
	source := newDocumentTestSource(t, fake)
	firstPart := bodyMeta(10, 1)

	first, err := source.resolveDocument(context.Background(), peer, firstPart)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	first.FileReference[0] = 99
	firstAgain, err := source.resolveDocument(context.Background(), peer, firstPart)
	if err != nil {
		t.Fatalf("resolve cached first: %v", err)
	}
	if firstAgain.FileReference[0] != 1 {
		t.Fatalf("cached FileReference = %v, want defensive copy", firstAgain.FileReference)
	}
	if _, err := source.resolveDocument(context.Background(), otherPeer, firstPart); err != nil {
		t.Fatalf("resolve changed peer: %v", err)
	}
	if _, err := source.resolveDocument(context.Background(), otherPeer, bodyMeta(10, 2)); err != nil {
		t.Fatalf("resolve changed stored size: %v", err)
	}
	if _, err := source.resolveDocument(context.Background(), otherPeer, bodyMeta(11, 2)); err != nil {
		t.Fatalf("resolve changed message: %v", err)
	}

	if got := fake.callCount(); got != len(resolvedSizes) {
		t.Fatalf("ResolveDocument calls = %d, want %d", got, len(resolvedSizes))
	}
}

func TestDocumentResolutionValidatesSizeBeforeCaching(t *testing.T) {
	peer := peerForChannel(testChannelID)
	fake := newCountingDocumentResolver(func(_ context.Context, resolvedPeer tgclient.InputPeer, msgID int64, call int) (tgclient.DocumentRef, error) {
		size := int64(2)
		if call > 1 {
			size = 1
		}
		return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: size}, nil
	})
	source := newDocumentTestSource(t, fake)
	part := bodyMeta(10, 1)

	if _, err := source.resolveDocument(context.Background(), peer, part); err == nil ||
		!strings.Contains(err.Error(), "size mismatch") || !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("wrong-size resolution error = %v, want size mismatch", err)
	}
	if _, err := source.resolveDocument(context.Background(), peer, part); err != nil {
		t.Fatalf("retry after wrong-size resolution: %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Fatalf("ResolveDocument calls = %d, want 2", got)
	}
}

func TestDocumentResolutionCoalescesAndCancelsEachWaiterIndependently(t *testing.T) {
	peer := peerForChannel(testChannelID)
	part := bodyMeta(10, 1)
	key := newDocumentRefKey(peer, part)
	started := make(chan struct{})
	release := make(chan struct{})
	resolverCanceled := make(chan struct{})
	fake := newCountingDocumentResolver(func(ctx context.Context, resolvedPeer tgclient.InputPeer, msgID int64, _ int) (tgclient.DocumentRef, error) {
		close(started)
		select {
		case <-release:
			return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: 1}, nil
		case <-ctx.Done():
			close(resolverCanceled)
			return tgclient.DocumentRef{}, ctx.Err()
		}
	})
	source := newDocumentTestSource(t, fake)

	canceledCtx, cancel := context.WithCancel(context.Background())
	firstResult := make(chan documentResolveResult, 1)
	go func() {
		ref, err := source.resolveDocument(canceledCtx, peer, part)
		firstResult <- documentResolveResult{ref: ref, err: err}
	}()
	waitForSignal(t, started, "document resolution")

	sharedResult := make(chan documentResolveResult, 1)
	go func() {
		ref, err := source.resolveDocument(context.Background(), peer, part)
		sharedResult <- documentResolveResult{ref: ref, err: err}
	}()
	waitForDocumentWaiters(t, source, key, 2)
	cancel()
	if got := waitForDocumentResult(t, firstResult); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", got.err)
	}
	select {
	case <-resolverCanceled:
		t.Fatal("shared resolution canceled while one waiter remained")
	default:
	}

	close(release)
	if got := waitForDocumentResult(t, sharedResult); got.err != nil {
		t.Fatalf("remaining waiter: %v", got.err)
	}
	if _, err := source.resolveDocument(context.Background(), peer, part); err != nil {
		t.Fatalf("cached resolution: %v", err)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("ResolveDocument calls = %d, want 1", got)
	}
}

func TestDocumentResolutionCancelsWhenLastWaiterLeavesAndDoesNotCache(t *testing.T) {
	peer := peerForChannel(testChannelID)
	part := bodyMeta(10, 1)
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	fake := newCountingDocumentResolver(func(ctx context.Context, resolvedPeer tgclient.InputPeer, msgID int64, call int) (tgclient.DocumentRef, error) {
		if call == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCanceled)
			return tgclient.DocumentRef{}, ctx.Err()
		}
		return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: 1}, nil
	})
	source := newDocumentTestSource(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan documentResolveResult, 1)
	go func() {
		ref, err := source.resolveDocument(ctx, peer, part)
		result <- documentResolveResult{ref: ref, err: err}
	}()
	waitForSignal(t, firstStarted, "first document resolution")
	cancel()
	if got := waitForDocumentResult(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("canceled resolution error = %v, want context.Canceled", got.err)
	}
	waitForSignal(t, firstCanceled, "abandoned document resolver cancellation")

	if _, err := source.resolveDocument(context.Background(), peer, part); err != nil {
		t.Fatalf("retry after abandoned resolution: %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Fatalf("ResolveDocument calls = %d, want 2", got)
	}
}

func TestAbandonedDocumentResolutionCannotCacheLateSuccess(t *testing.T) {
	peer := peerForChannel(testChannelID)
	part := bodyMeta(10, 1)
	firstStarted := make(chan struct{})
	allowLateSuccess := make(chan struct{})
	firstReturned := make(chan struct{})
	fake := newCountingDocumentResolver(func(_ context.Context, resolvedPeer tgclient.InputPeer, msgID int64, call int) (tgclient.DocumentRef, error) {
		if call == 1 {
			close(firstStarted)
			<-allowLateSuccess
			close(firstReturned)
		}
		return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: 1}, nil
	})
	source := newDocumentTestSource(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan documentResolveResult, 1)
	go func() {
		ref, err := source.resolveDocument(ctx, peer, part)
		result <- documentResolveResult{ref: ref, err: err}
	}()
	waitForSignal(t, firstStarted, "first document resolution")
	cancel()
	if got := waitForDocumentResult(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("canceled resolution error = %v, want context.Canceled", got.err)
	}
	close(allowLateSuccess)
	waitForSignal(t, firstReturned, "late document resolution")
	drain, cancelDrain := closeContext()
	defer cancelDrain()
	if err := source.documentResolutions.Wait(drain); err != nil {
		t.Fatalf("drain late resolution: %v", err)
	}

	if _, err := source.resolveDocument(context.Background(), peer, part); err != nil {
		t.Fatalf("retry after late success: %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Fatalf("ResolveDocument calls = %d, want 2", got)
	}
}

func TestDocumentResolutionDoesNotCacheErrors(t *testing.T) {
	sentinel := errors.New("temporary Telegram failure")
	peer := peerForChannel(testChannelID)
	part := bodyMeta(10, 1)
	fake := newCountingDocumentResolver(func(_ context.Context, resolvedPeer tgclient.InputPeer, msgID int64, call int) (tgclient.DocumentRef, error) {
		if call == 1 {
			return tgclient.DocumentRef{}, sentinel
		}
		return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: 1}, nil
	})
	source := newDocumentTestSource(t, fake)

	if _, err := source.resolveDocument(context.Background(), peer, part); !errors.Is(err, sentinel) {
		t.Fatalf("first resolution error = %v, want sentinel", err)
	}
	if _, err := source.resolveDocument(context.Background(), peer, part); err != nil {
		t.Fatalf("retry after resolution error: %v", err)
	}
	if got := fake.callCount(); got != 2 {
		t.Fatalf("ResolveDocument calls = %d, want 2", got)
	}
}

func TestOpenerCloseCancelsDocumentResolutionAndClearsCache(t *testing.T) {
	peer := peerForChannel(testChannelID)
	cachedPart := bodyMeta(10, 1)
	blockedPart := bodyMeta(11, 1)
	blockedStarted := make(chan struct{})
	blockedFinished := make(chan struct{})
	fake := newCountingDocumentResolver(func(ctx context.Context, resolvedPeer tgclient.InputPeer, msgID int64, _ int) (tgclient.DocumentRef, error) {
		if msgID == blockedPart.BodyMsgID {
			close(blockedStarted)
			<-ctx.Done()
			close(blockedFinished)
			return tgclient.DocumentRef{}, ctx.Err()
		}
		return tgclient.DocumentRef{Peer: resolvedPeer, MsgID: msgID, Size: 1}, nil
	})
	source := newDocumentTestSource(t, fake)
	if _, err := source.resolveDocument(context.Background(), peer, cachedPart); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	result := make(chan documentResolveResult, 1)
	go func() {
		ref, err := source.resolveDocument(context.Background(), peer, blockedPart)
		result <- documentResolveResult{ref: ref, err: err}
	}()
	waitForSignal(t, blockedStarted, "blocked document resolution")
	closeCtx, cancel := closeContext()
	defer cancel()
	if err := source.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForSignal(t, blockedFinished, "document resolver shutdown")
	if got := waitForDocumentResult(t, result); !errors.Is(got.err, ErrClosed) {
		t.Fatalf("resolution after Close error = %v, want ErrClosed", got.err)
	}
	if got := source.documentCache.Len(); got != 0 {
		t.Fatalf("cached documents after Close = %d, want 0", got)
	}
	if got := source.documentResolutions.Len(); got != 0 {
		t.Fatalf("in-flight documents after Close = %d, want 0", got)
	}
}

type documentResolveResult struct {
	ref tgclient.DocumentRef
	err error
}

type countingDocumentResolver struct {
	mu      sync.Mutex
	calls   int
	resolve func(context.Context, tgclient.InputPeer, int64, int) (tgclient.DocumentRef, error)
}

func newCountingDocumentResolver(resolve func(context.Context, tgclient.InputPeer, int64, int) (tgclient.DocumentRef, error)) *countingDocumentResolver {
	return &countingDocumentResolver{resolve: resolve}
}

func (r *countingDocumentResolver) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	resolve := r.resolve
	r.mu.Unlock()
	return resolve(ctx, peer, msgID, call)
}

func (r *countingDocumentResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type staticPeers struct {
	peer tgclient.InputPeer
}

func (p staticPeers) ResolvePeer(context.Context, int64) (tgclient.InputPeer, error) {
	return p.peer, nil
}

type emptyStored struct{}

func (emptyStored) ReadStoredAt(context.Context, tgclient.DocumentRef, []byte, int64) (int, error) {
	return 0, io.EOF
}

func newDocumentTestSource(t *testing.T, documents DocumentResolver) *StoredSource {
	t.Helper()
	return newTestSource(t, SourceDependencies{
		Peers:     staticPeers{peer: peerForChannel(testChannelID)},
		Documents: documents,
		Stored:    emptyStored{},
		Clock:     clock.NewFake(time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)),
	})
}

func documentRefFor(key documentRefKey, documentID int64) tgclient.DocumentRef {
	return tgclient.DocumentRef{
		Peer:          key.peer,
		MsgID:         key.msgID,
		Size:          key.storedSize,
		DocumentID:    documentID,
		FileReference: []byte{byte(documentID)},
	}
}

func waitForDocumentResult(t *testing.T, results <-chan documentResolveResult) documentResolveResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(resolverEventTimeout):
		t.Fatal("timed out waiting for document resolution")
		return documentResolveResult{}
	}
}

func waitForDocumentWaiters(t *testing.T, source *StoredSource, key documentRefKey, want int) {
	t.Helper()
	deadline := time.After(resolverEventTimeout)
	for {
		got := source.documentResolutions.Waiters(key)
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("document resolution waiters = %d, want %d", got, want)
		default:
			runtime.Gosched()
		}
	}
}
