package conn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type refreshServer struct {
	calls     atomic.Int32
	gate      chan struct{}
	reference atomic.Pointer[[]byte]
	missing   bool
}

func newRefreshServer() *refreshServer {
	s := &refreshServer{}
	s.setReference([]byte{1})
	return s
}

func (s *refreshServer) setReference(ref []byte) { s.reference.Store(&ref) }

func (s *refreshServer) handle(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	request, ok := input.(*tg.ChannelsGetMessagesRequest)
	if !ok {
		return errors.New("unexpected request")
	}
	s.calls.Add(1)
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	reply := &tg.MessagesChannelMessages{}
	if !s.missing {
		id := request.ID[0].(*tg.InputMessageID).ID
		doc := &tg.Document{ID: 50, AccessHash: 60, DCID: 4, FileReference: *s.reference.Load()}
		reply.Messages = append(reply.Messages, documentMessage(id, doc))
	}
	return respondWith(output, reply)
}

func TestFileReferenceRefreshDeduplicated(t *testing.T) {
	server := newRefreshServer()
	server.gate = make(chan struct{})
	c, _ := newTestConnection(t, newFakeClient(server.handle))

	const readers = 8
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Go(func() {
			id, hash, ref, dc, err := c.RefreshDocument(context.Background(), 10, 20, 7)
			if err != nil || id != 50 || hash != 60 || dc != 4 || string(ref) != "\x01" {
				t.Errorf("RefreshDocument = %d %d %v %d, %v", id, hash, ref, dc, err)
			}
		})
	}
	waitFor(t, func() bool { return c.refreshes.waiting("10:7") == readers })
	close(server.gate)
	wg.Wait()

	if got := server.calls.Load(); got != 1 {
		t.Fatalf("getMessages calls = %d, want 1", got)
	}
}

func TestFileReferenceRefreshReturnsTheFreshReference(t *testing.T) {
	server := newRefreshServer()
	c, _ := newTestConnection(t, newFakeClient(server.handle))

	_, _, first, _, err := c.RefreshDocument(context.Background(), 10, 20, 7)
	if err != nil || string(first) != "\x01" {
		t.Fatalf("first refresh = %v, %v", first, err)
	}
	server.setReference([]byte{2})
	_, _, second, _, err := c.RefreshDocument(context.Background(), 10, 20, 7)
	if err != nil || string(second) != "\x02" {
		t.Fatalf("second refresh = %v, %v", second, err)
	}
	if got := server.calls.Load(); got != 2 {
		t.Fatalf("sequential refreshes made %d calls, want 2", got)
	}
}

func TestFileReferenceRefreshKeysByChannelAndMessage(t *testing.T) {
	server := newRefreshServer()
	server.gate = make(chan struct{})
	c, _ := newTestConnection(t, newFakeClient(server.handle))

	var wg sync.WaitGroup
	for _, msgID := range []int64{7, 8} {
		wg.Go(func() {
			if _, _, _, _, err := c.RefreshDocument(context.Background(), 10, 20, msgID); err != nil {
				t.Errorf("RefreshDocument: %v", err)
			}
		})
	}
	waitFor(t, func() bool { return c.refreshes.waiting("10:7") == 1 && c.refreshes.waiting("10:8") == 1 })
	close(server.gate)
	wg.Wait()

	if got := server.calls.Load(); got != 2 {
		t.Fatalf("distinct messages made %d calls, want 2", got)
	}
}

func TestFileReferenceRefreshWaiterCancelDoesNotFailOthers(t *testing.T) {
	server := newRefreshServer()
	server.gate = make(chan struct{})
	c, _ := newTestConnection(t, newFakeClient(server.handle))

	canceled, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _, _, _, err := c.RefreshDocument(canceled, 10, 20, 7)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled waiter err = %v, want context.Canceled", err)
		}
	})
	waitFor(t, func() bool { return c.refreshes.waiting("10:7") == 1 })
	wg.Go(func() {
		if _, _, _, _, err := c.RefreshDocument(context.Background(), 10, 20, 7); err != nil {
			t.Errorf("surviving waiter: %v", err)
		}
	})
	waitFor(t, func() bool { return c.refreshes.waiting("10:7") == 2 })
	cancel()
	close(server.gate)
	wg.Wait()
}

func TestFileReferenceRefreshReportsMissingMessage(t *testing.T) {
	server := newRefreshServer()
	server.missing = true
	c, _ := newTestConnection(t, newFakeClient(server.handle))

	_, _, _, _, err := c.RefreshDocument(context.Background(), 10, 20, 7)
	if !errors.Is(err, tgclient.ErrMessageNotFound) {
		t.Fatalf("err = %v, want %v", err, tgclient.ErrMessageNotFound)
	}
}
