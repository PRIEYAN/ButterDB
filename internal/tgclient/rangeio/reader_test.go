package rangeio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func documentRef() tgclient.DocumentRef {
	return tgclient.DocumentRef{Peer: testPeer, MsgID: 5, Size: 8192, DocumentID: 11, AccessHash: 12, FileReference: []byte("v1"), DCID: 2}
}

func serveFile(data []byte, fileReference string, requests *[]*tg.UploadGetFileRequest, mu *sync.Mutex) func(context.Context, bin.Encoder, bin.Decoder) error {
	return func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request, ok := input.(*tg.UploadGetFileRequest)
		if !ok {
			return errors.New("unexpected request")
		}
		mu.Lock()
		*requests = append(*requests, request)
		mu.Unlock()
		location := request.Location.(*tg.InputDocumentFileLocation)
		if string(location.FileReference) != fileReference {
			return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
		}
		end := min(request.Offset+int64(request.Limit), int64(len(data)))
		output.(*tg.UploadFileBox).File = &tg.UploadFile{Bytes: data[request.Offset:end]}
		return nil
	}
}

func TestRangePreconditions(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile(make([]byte, 4096), "v1", &requests, &mu))
	tests := []struct {
		name   string
		offset int64
		length int
		want   error
	}{
		{name: "negative offset", offset: -4096, length: 16, want: errs.ErrInvalidRange},
		{name: "misaligned offset", offset: 100, length: 16, want: errs.ErrInvalidRange},
		{name: "longer than one mebibyte", offset: 0, length: RangeReadMaxBytes + 1, want: errs.ErrInvalidRange},
		{name: "crosses a one mebibyte boundary", offset: int64(RangeReadMaxBytes) - 4096, length: 8192, want: errs.ErrInvalidRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			n, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), test.offset, make([]byte, test.length))
			if n != 0 || !errors.Is(err, test.want) {
				t.Fatalf("ReadDocumentRange = (%d, %v), want %v", n, err, test.want)
			}
		})
	}
	if len(requests) != 0 || len(h.invoker.routes()) != 0 || h.gate.acquired != 0 {
		t.Fatalf("a rejected range reached the wire: requests=%d routes=%d slots=%d", len(requests), len(h.invoker.routes()), h.gate.acquired)
	}
	n, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, nil)
	if n != 0 || err != nil {
		t.Fatalf("empty read = (%d, %v), want (0, nil)", n, err)
	}
	accepted := []struct {
		name   string
		offset int64
		length int
	}{
		{name: "one byte at zero", offset: 0, length: 1},
		{name: "full block", offset: 0, length: RangeReadMaxBytes},
		{name: "ends exactly at the boundary", offset: int64(RangeReadMaxBytes) - 4096, length: 4096},
	}
	for _, test := range accepted {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRange(test.offset, test.length); err != nil {
				t.Fatalf("validateRange(%d, %d) = %v", test.offset, test.length, err)
			}
		})
	}
}

func TestReadDocumentRangeRoundsLimitAndCopiesRequestedBytes(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 512)
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile(data, "v1", &requests, &mu))
	dst := make([]byte, 100)
	n, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 4096, dst)
	if err != nil || n != 100 || !bytes.Equal(dst, data[4096:4196]) {
		t.Fatalf("read = (%d, %v)", n, err)
	}
	request := requests[0]
	if request.Limit != int(RangeReadAlignment) || request.Offset != 4096 {
		t.Fatalf("request offset=%d limit=%d, want 4096 and one alignment unit", request.Offset, request.Limit)
	}
	if !request.Precise || !request.CDNSupported {
		t.Fatalf("request precise=%v cdn=%v, want both set", request.Precise, request.CDNSupported)
	}
	if h.gate.acquired != 1 || h.gate.held != 0 {
		t.Fatalf("slots acquired=%d held=%d, want 1 and 0", h.gate.acquired, h.gate.held)
	}
	if routes := h.invoker.routes(); len(routes) != 1 || !routes[0].media || routes[0].dc != 2 {
		t.Fatalf("routes = %+v, want one media call on dc 2", routes)
	}
}

func TestReadDocumentRangeShortTailIsUnexpectedEOF(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abc"), "v1", &requests, &mu))
	n, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, make([]byte, 10))
	if n != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read = (%d, %v), want unexpected EOF", n, err)
	}
}

func TestReadDocumentRangeUnknownDCUsesPrimaryConnection(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abcdefgh"), "v1", &requests, &mu))
	ref := documentRef()
	ref.DCID = 0
	if _, err := h.reader.ReadDocumentRange(context.Background(), ref, 0, make([]byte, 8)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if routes := h.invoker.routes(); len(routes) != 1 || routes[0].media {
		t.Fatalf("routes = %+v, want one primary call", routes)
	}
}

func TestReadDocumentRangeFollowsFileMigrateOnPrimary(t *testing.T) {
	calls := 0
	h := newReaderHarness(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		calls++
		if calls == 1 {
			return tgerr.New(303, "FILE_MIGRATE_4")
		}
		output.(*tg.UploadFileBox).File = &tg.UploadFile{Bytes: []byte("abcdefgh")}
		return nil
	})
	dst := make([]byte, 8)
	if _, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, dst); err != nil || string(dst) != "abcdefgh" {
		t.Fatalf("read = %q, %v", dst, err)
	}
	routes := h.invoker.routes()
	if len(routes) != 2 || !routes[0].media || routes[1].media {
		t.Fatalf("routes = %+v, want media then primary", routes)
	}
}

func TestReadDocumentRangeRefreshesExpiredReferenceOnce(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abcdefgh"), "v2", &requests, &mu))
	dst := make([]byte, 8)
	if _, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, dst); err != nil || string(dst) != "abcdefgh" {
		t.Fatalf("first read = %q, %v", dst, err)
	}
	if _, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, dst); err != nil {
		t.Fatalf("second read with the same stale reference: %v", err)
	}
	if h.refresher.calls != 1 {
		t.Fatalf("refreshed %d times, want 1", h.refresher.calls)
	}
	if len(requests) != 3 {
		t.Fatalf("wire reads = %d, want stale, fresh, fresh", len(requests))
	}
}

func TestFileReferenceRefreshDeduplicatedAcrossReaders(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abcdefgh"), "v2", &requests, &mu))
	var wg sync.WaitGroup
	errsSeen := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, make([]byte, 8))
			errsSeen <- err
		}()
	}
	wg.Wait()
	close(errsSeen)
	for err := range errsSeen {
		if err != nil {
			t.Fatalf("concurrent read: %v", err)
		}
	}
	if h.refresher.calls != 1 {
		t.Fatalf("refreshed %d times for 16 concurrent readers, want 1", h.refresher.calls)
	}
}

func TestReadDocumentRangeReportsExpiredReferenceWhenFreshOneIsRejected(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abcdefgh"), "never", &requests, &mu))
	_, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, make([]byte, 8))
	if !errors.Is(err, tgclient.ErrFileReferenceExpired) {
		t.Fatalf("error = %v, want ErrFileReferenceExpired", err)
	}
	if h.refresher.calls != 1 || len(requests) != 2 {
		t.Fatalf("refreshes=%d reads=%d, want exactly one refresh and one repeat", h.refresher.calls, len(requests))
	}
}

func TestReadDocumentRangeRefreshFailureKeepsBothFacts(t *testing.T) {
	var requests []*tg.UploadGetFileRequest
	var mu sync.Mutex
	h := newReaderHarness(t, serveFile([]byte("abcdefgh"), "v2", &requests, &mu))
	boom := errors.New("lookup failed")
	h.refresher.err = boom
	_, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, make([]byte, 8))
	if !errors.Is(err, tgclient.ErrFileReferenceExpired) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, want expired reference wrapping the lookup failure", err)
	}
}

func TestReadDocumentRangeFloodWaitIsTyped(t *testing.T) {
	h := newReaderHarness(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		return tgerr.New(420, "FLOOD_PREMIUM_WAIT_9")
	})
	_, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), 0, make([]byte, 8))
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || flood.Duration != 9*time.Second {
		t.Fatalf("error = %v, want a 9s FloodWaitError", err)
	}
	if h.gate.held != 0 {
		t.Fatal("slot leaked on a flood wait")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	valid := func() Dependencies {
		return Dependencies{
			Invoker:   newRoutingInvoker(nil),
			Gate:      &countingGate{},
			Refresher: &scriptedRefresher{},
			Logger:    discardLogger(),
		}
	}
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{name: "invoker", mutate: func(d *Dependencies) { d.Invoker = nil }},
		{name: "gate", mutate: func(d *Dependencies) { d.Gate = nil }},
		{name: "refresher", mutate: func(d *Dependencies) { d.Refresher = nil }},
		{name: "logger", mutate: func(d *Dependencies) { d.Logger = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := valid()
			test.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func channelMessage(id int, docID int64) tg.MessageClass {
	return &tg.Message{ID: id, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: docID, AccessHash: 9, FileReference: []byte("r"), DCID: 2, Size: int64(id) * 10}}}
}

func TestResolveDocumentsPreservesRequestedOrder(t *testing.T) {
	h := newReaderHarness(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request := input.(*tg.ChannelsGetMessagesRequest)
		channel := request.Channel.(*tg.InputChannel)
		if channel.ChannelID != testPeer.ChannelID || channel.AccessHash != testPeer.AccessHash {
			t.Fatalf("channel = %+v", channel)
		}
		var found []tg.MessageClass
		for i := len(request.ID) - 1; i >= 0; i-- {
			id := request.ID[i].(*tg.InputMessageID).ID
			found = append(found, channelMessage(id, int64(id)+1000))
		}
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: found}
		return nil
	})
	refs, err := h.reader.ResolveDocuments(context.Background(), testPeer, []int64{7, 3, 9})
	if err != nil {
		t.Fatalf("ResolveDocuments: %v", err)
	}
	for i, want := range []int64{7, 3, 9} {
		if refs[i].MsgID != want || refs[i].DocumentID != want+1000 || refs[i].Size != want*10 || refs[i].Peer != testPeer {
			t.Fatalf("refs[%d] = %+v, want message %d", i, refs[i], want)
		}
	}
}

func TestResolveDocumentsBatchesOfOneHundred(t *testing.T) {
	var sizes []int
	h := newReaderHarness(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request := input.(*tg.ChannelsGetMessagesRequest)
		sizes = append(sizes, len(request.ID))
		var found []tg.MessageClass
		for _, id := range request.ID {
			found = append(found, channelMessage(id.(*tg.InputMessageID).ID, 1))
		}
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: found}
		return nil
	})
	ids := make([]int64, 250)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	refs, err := h.reader.ResolveDocuments(context.Background(), testPeer, ids)
	if err != nil || len(refs) != 250 {
		t.Fatalf("ResolveDocuments = %d refs, %v", len(refs), err)
	}
	if len(sizes) != 3 || sizes[0] != 100 || sizes[1] != 100 || sizes[2] != 50 {
		t.Fatalf("batch sizes = %v, want 100, 100, 50", sizes)
	}
}

func TestResolveDocumentsFailures(t *testing.T) {
	tests := []struct {
		name     string
		peer     tgclient.InputPeer
		messages []tg.MessageClass
		callErr  error
		want     error
	}{
		{name: "zero access hash", peer: tgclient.InputPeer{ChannelID: 1}, want: tgclient.ErrZeroAccessHash},
		{name: "deleted message", peer: testPeer, messages: []tg.MessageClass{&tg.MessageEmpty{ID: 4}}, want: tgclient.ErrMessageNotFound},
		{name: "absent message", peer: testPeer, want: tgclient.ErrMessageNotFound},
		{name: "text message", peer: testPeer, messages: []tg.MessageClass{&tg.Message{ID: 4}}, want: ErrNotDocument},
		{name: "unavailable channel", peer: testPeer, callErr: tgerr.New(400, "CHANNEL_PRIVATE"), want: tgclient.ErrChannelUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newReaderHarness(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
				if test.callErr != nil {
					return test.callErr
				}
				output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: test.messages}
				return nil
			})
			_, err := h.reader.ResolveDocument(context.Background(), test.peer, 4)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveDocumentsFailsWholeBatchOnOneMissing(t *testing.T) {
	h := newReaderHarness(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{channelMessage(1, 1)}}
		return nil
	})
	refs, err := h.reader.ResolveDocuments(context.Background(), testPeer, []int64{1, 2})
	if refs != nil || !errors.Is(err, tgclient.ErrMessageNotFound) {
		t.Fatalf("ResolveDocuments = (%v, %v), want no partial result", refs, err)
	}
}

func TestIsFileReferenceError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "expired", err: tgerr.New(400, "FILE_REFERENCE_EXPIRED"), want: true},
		{name: "empty", err: tgerr.New(400, "FILE_REFERENCE_EMPTY"), want: true},
		{name: "invalid", err: tgerr.New(400, "FILE_REFERENCE_INVALID"), want: true},
		{name: "typed sentinel", err: tgclient.ErrFileReferenceExpired, want: true},
		{name: "other rpc error", err: tgerr.New(400, "FILE_ID_INVALID"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsFileReferenceError(test.err); got != test.want {
				t.Fatalf("IsFileReferenceError = %v, want %v", got, test.want)
			}
		})
	}
}

var _ tgclient.RangeReader = (*Reader)(nil)
