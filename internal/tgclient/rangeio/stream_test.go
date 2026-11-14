package rangeio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gotd/td/rpc"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type blockSource struct {
	mu       sync.Mutex
	data     []byte
	ref      tgclient.DocumentRef
	reads    map[int64]int
	failures map[int64][]error
	resolves int
}

func newBlockSource(size int64) *blockSource {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + i/int(StreamBlockBytes))
	}
	return &blockSource{
		data:     data,
		ref:      tgclient.DocumentRef{Peer: testPeer, MsgID: 7, Size: size, FileReference: []byte("v1")},
		reads:    map[int64]int{},
		failures: map[int64][]error{},
	}
}

func (s *blockSource) ReadDocumentRange(_ context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[offset]++
	if queued := s.failures[offset]; len(queued) > 0 {
		s.failures[offset] = queued[1:]
		return 0, queued[0]
	}
	if !bytes.Equal(ref.FileReference, s.ref.FileReference) {
		return 0, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	}
	return copy(dst, s.data[offset:]), nil
}

func (s *blockSource) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolves++
	return s.ref, nil
}

func (s *blockSource) expireReference() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ref.FileReference = []byte("v2")
}

func (s *blockSource) stream(t *testing.T, retry *scriptedBlockRetry) *Stream {
	t.Helper()
	stream, err := NewStream(StreamDependencies{Blocks: s, Resolver: s, Retry: retry})
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	return stream
}

func initialRef(src *blockSource) tgclient.DocumentRef {
	ref := src.ref
	ref.Size = int64(len(src.data))
	return ref
}

func TestDocumentDownloadStreamsInOrder(t *testing.T) {
	src := newBlockSource(StreamBlockBytes + 12345)
	src.failures[StreamBlockBytes] = []error{fmt.Errorf("acquire connection: DC closed: %w", context.Canceled)}
	retry := &scriptedBlockRetry{maxRetries: 6}
	stream := src.stream(t, retry)
	var out bytes.Buffer
	if err := stream.Copy(context.Background(), initialRef(src), &out); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !bytes.Equal(out.Bytes(), src.data) {
		t.Fatal("streamed bytes differ from the document")
	}
	if stream.Retries() != 1 {
		t.Fatalf("retries = %d, want 1", stream.Retries())
	}
	for offset, n := range src.reads {
		if n > 2 || (offset == 0 && n != 1) {
			t.Fatalf("block %d read %d times", offset, n)
		}
	}
}

func TestDocumentDownloadRetriesOnlyTheFailedBlock(t *testing.T) {
	src := newBlockSource(3 * StreamBlockBytes)
	dropped := fmt.Errorf("get next chunk: invoke pool: rpcDoRequest: %w", rpc.ErrEngineClosed)
	src.failures[StreamBlockBytes] = []error{dropped, dropped}
	retry := &scriptedBlockRetry{maxRetries: 6}
	stream := src.stream(t, retry)
	var out bytes.Buffer
	if err := stream.Copy(context.Background(), initialRef(src), &out); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !bytes.Equal(out.Bytes(), src.data) {
		t.Fatal("downloaded bytes differ from the document")
	}
	if src.reads[StreamBlockBytes] != 3 || src.reads[0] != 1 || src.reads[2*StreamBlockBytes] != 1 {
		t.Fatalf("reads = %v, want only the failed block repeated", src.reads)
	}
	if stream.Retries() != 2 || retry.slept != 2 {
		t.Fatalf("retries=%d backoffs=%d, want 2 and 2", stream.Retries(), retry.slept)
	}
}

func TestDocumentDownloadRefreshesAnExpiredReferenceOnce(t *testing.T) {
	src := newBlockSource(2 * StreamBlockBytes)
	retry := &scriptedBlockRetry{maxRetries: 6}
	stream := src.stream(t, retry)
	stale := initialRef(src)
	src.expireReference()
	var out bytes.Buffer
	if err := stream.Copy(context.Background(), stale, &out); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !bytes.Equal(out.Bytes(), src.data) {
		t.Fatal("downloaded bytes differ from the document")
	}
	if src.resolves != 1 {
		t.Fatalf("resolved %d times, want 1", src.resolves)
	}
	if src.reads[0] != 2 || src.reads[StreamBlockBytes] != 1 {
		t.Fatalf("reads = %v, want the first block twice and the second once", src.reads)
	}
	if retry.slept != 0 || stream.Retries() != 0 {
		t.Fatal("a refresh must not count as a retry")
	}
}

func TestDocumentDownloadFailsFastOnADefiniteRejection(t *testing.T) {
	src := newBlockSource(StreamBlockBytes)
	src.failures[0] = []error{tgerr.New(400, "FILE_ID_INVALID")}
	retry := &scriptedBlockRetry{maxRetries: 6}
	err := src.stream(t, retry).Copy(context.Background(), initialRef(src), &bytes.Buffer{})
	if !tgerr.Is(err, "FILE_ID_INVALID") {
		t.Fatalf("err = %v, want FILE_ID_INVALID", err)
	}
	if src.reads[0] != 1 || retry.slept != 0 {
		t.Fatalf("definite rejection was retried: reads=%d backoffs=%d", src.reads[0], retry.slept)
	}
}

func TestDocumentDownloadGivesUpAfterTheBlockBudget(t *testing.T) {
	src := newBlockSource(StreamBlockBytes)
	dropped := fmt.Errorf("rpcDoRequest: retryUntilAck: %w", rpc.ErrEngineClosed)
	for range 10 {
		src.failures[0] = append(src.failures[0], dropped)
	}
	retry := &scriptedBlockRetry{maxRetries: 6}
	err := src.stream(t, retry).Copy(context.Background(), initialRef(src), &bytes.Buffer{})
	if err == nil || !errors.Is(err, rpc.ErrEngineClosed) {
		t.Fatalf("err = %v, want the transport error", err)
	}
	if src.reads[0] != retry.maxRetries+1 {
		t.Fatalf("block attempted %d times, want %d", src.reads[0], retry.maxRetries+1)
	}
}

func TestStreamPropagatesWriterFailure(t *testing.T) {
	src := newBlockSource(StreamBlockBytes)
	retry := &scriptedBlockRetry{maxRetries: 6}
	boom := errors.New("disk full")
	err := src.stream(t, retry).Copy(context.Background(), initialRef(src), failingWriter{err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the writer error", err)
	}
}

func TestNewStreamRejectsMissingDependencies(t *testing.T) {
	src := newBlockSource(1)
	retry := &scriptedBlockRetry{}
	tests := []struct {
		name string
		deps StreamDependencies
	}{
		{name: "blocks", deps: StreamDependencies{Resolver: src, Retry: retry}},
		{name: "resolver", deps: StreamDependencies{Blocks: src, Retry: retry}},
		{name: "retry", deps: StreamDependencies{Blocks: src, Resolver: src}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewStream(test.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewStream error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
