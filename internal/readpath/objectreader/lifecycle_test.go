package objectreader

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type readerOpenResult struct {
	reader Opened
	err    error
}

func TestOpenerCloseDuringResolutionPreventsReaderPublication(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
	started := make(chan struct{})
	release := make(chan struct{})
	tg.resolveHook = func(_ context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
		close(started)
		<-release
		return tgclient.DocumentRef{Peer: peer, MsgID: msgID, Size: 1}, nil
	}
	opener := newDefaultOpener(t, tg, nil)

	result := make(chan readerOpenResult, 1)
	go func() {
		reader, err := opener.Open(context.Background(), plainMeta(10, []byte("x")))
		result <- readerOpenResult{reader: reader, err: err}
	}()
	waitForSignal(t, started, "document resolution")

	closed := make(chan error, 1)
	go func() {
		ctx, cancel := closeContext()
		defer cancel()
		closed <- opener.Close(ctx)
	}()
	select {
	case got := <-result:
		if got.reader != nil || !errors.Is(got.err, ErrClosed) {
			t.Fatalf("Open after overlapping Close = reader %v, err %v; want nil/ErrClosed", got.reader, got.err)
		}
	case <-time.After(resolverEventTimeout):
		t.Fatal("timed out waiting for Open to observe Close")
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenerResolutionLimitIsSharedAcrossOpensAndCloseCancelsWaiters(t *testing.T) {
	bodies := make(map[int64][]byte, 5)
	for msgID := int64(1); msgID <= 5; msgID++ {
		bodies[msgID] = []byte{byte(msgID)}
	}
	tg := newFakeTelegram(bodies)

	started := make(chan int64, 5)
	var mu sync.Mutex
	active, maximum, resolveCalls := 0, 0, 0
	tg.resolveHook = func(ctx context.Context, _ tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
		mu.Lock()
		active++
		resolveCalls++
		maximum = max(maximum, active)
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		started <- msgID
		<-ctx.Done()
		return tgclient.DocumentRef{}, ctx.Err()
	}
	opener := newDefaultOpener(t, tg, nil)

	results := make(chan error, 5)
	for msgID := int64(1); msgID <= 4; msgID++ {
		go func() {
			_, openErr := opener.Open(context.Background(), plainMeta(msgID, bodies[msgID]))
			results <- openErr
		}()
	}
	waitForResolveStarts(t, started, 4)
	if got := cap(opener.source.resolveSlots); got != maxConcurrentDocumentResolutions {
		t.Fatalf("resolution slot capacity = %d, want %d", got, maxConcurrentDocumentResolutions)
	}
	if got := len(opener.source.resolveSlots); got != maxConcurrentDocumentResolutions {
		t.Fatalf("occupied resolution slots = %d, want %d", got, maxConcurrentDocumentResolutions)
	}

	go func() {
		_, openErr := opener.Open(context.Background(), plainMeta(5, bodies[5]))
		results <- openErr
	}()
	closeCtx, cancel := closeContext()
	defer cancel()
	if err := opener.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for range 5 {
		if openErr := waitForOpenResult(t, results); !errors.Is(openErr, ErrClosed) {
			t.Fatalf("Open interrupted by Close error = %v, want ErrClosed", openErr)
		}
	}
	mu.Lock()
	gotMaximum, gotCalls := maximum, resolveCalls
	mu.Unlock()
	if gotMaximum != maxConcurrentDocumentResolutions || gotCalls != maxConcurrentDocumentResolutions {
		t.Fatalf("resolution concurrency max=%d calls=%d, want %d/%d", gotMaximum, gotCalls, maxConcurrentDocumentResolutions, maxConcurrentDocumentResolutions)
	}
}

func TestOpenerRejectsEqualSizeDocumentWithWrongIdentity(t *testing.T) {
	expectedPeer := testPeer()
	tests := []struct {
		name string
		ref  tgclient.DocumentRef
	}{
		{name: "message", ref: tgclient.DocumentRef{Peer: expectedPeer, MsgID: 99, Size: 1}},
		{
			name: "peer",
			ref: tgclient.DocumentRef{
				Peer:  tgclient.InputPeer{ChannelID: expectedPeer.ChannelID + 1, AccessHash: expectedPeer.AccessHash},
				MsgID: 10,
				Size:  1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
			tg.resolveHook = func(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
				return tt.ref, nil
			}
			opener := newDefaultOpener(t, tg, nil)
			reader, openErr := opener.Open(context.Background(), plainMeta(10, []byte("x")))
			if reader != nil || !errors.Is(openErr, ErrDocumentMismatch) {
				t.Fatalf("Open wrong identity = reader %v, err %v; want nil/ErrDocumentMismatch", reader, openErr)
			}
		})
	}
}

func TestExportedOperationsRejectNilContext(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
	opener := newDefaultOpener(t, tg, nil)
	meta := plainMeta(10, []byte("x"))

	if reader, err := opener.Open(nil, meta); reader != nil || !errors.Is(err, ErrNilContext) {
		t.Fatalf("Open(nil) = reader %v, err %v; want nil/ErrNilContext", reader, err)
	}
	reader := mustOpenReader(t, opener, meta)
	if n, readErr := reader.ReadAt(nil, nil, 0); n != 0 || !errors.Is(readErr, ErrNilContext) {
		t.Fatalf("ReadAt(nil) = n %d, err %v; want 0/ErrNilContext", n, readErr)
	}
	if n, readErr := opener.source.ReadDocumentRange(nil, meta, 0, make([]byte, 1)); n != 0 || !errors.Is(readErr, ErrNilContext) {
		t.Fatalf("ReadDocumentRange(nil) = n %d, err %v; want 0/ErrNilContext", n, readErr)
	}
}

func TestReaderCloseIsIndependentAndPreventsFurtherReads(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
	opener := newDefaultOpener(t, tg, nil)
	meta := plainMeta(10, []byte("x"))
	first := mustOpenReader(t, opener, meta)
	second := mustOpenReader(t, opener, meta)

	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first again: %v", err)
	}
	if _, readErr := first.ReadAt(context.Background(), make([]byte, 1), 0); !errors.Is(readErr, ErrReaderClosed) {
		t.Fatalf("closed reader error = %v, want ErrReaderClosed", readErr)
	}
	buf := make([]byte, 1)
	if n, readErr := second.ReadAt(context.Background(), buf, 0); n != 1 || readErr != nil || string(buf) != "x" {
		t.Fatalf("independent reader = %q, n %d, err %v; want x/1/nil", buf, n, readErr)
	}

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("Close opener: %v", err)
	}
	if _, readErr := second.ReadAt(context.Background(), make([]byte, 1), 0); !errors.Is(readErr, ErrClosed) {
		t.Fatalf("reader after opener Close error = %v, want ErrClosed", readErr)
	}
}

func TestOpenerCloseDrainsResolutionGoroutines(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
	started := make(chan struct{})
	release := make(chan struct{})
	tg.resolveHook = func(_ context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
		close(started)
		<-release
		return tgclient.DocumentRef{Peer: peer, MsgID: msgID, Size: 1}, nil
	}
	opener := newDefaultOpener(t, tg, nil)
	go func() {
		_, _ = opener.Open(context.Background(), plainMeta(10, []byte("x")))
	}()
	waitForSignal(t, started, "document resolution")

	expired, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := opener.Close(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close with a stuck resolver error = %v, want deadline exceeded", err)
	}
	close(release)
	drain, cancelDrain := closeContext()
	defer cancelDrain()
	if err := opener.source.documentResolutions.Wait(drain); err != nil {
		t.Fatalf("resolver goroutine did not finish: %v", err)
	}
}
