package rangereader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type gatedRangeFake struct {
	mu       sync.Mutex
	data     []byte
	gates    map[int64]chan struct{}
	outcomes map[int64]error
	entered  chan int64
}

func newGatedRangeFake(data []byte) *gatedRangeFake {
	return &gatedRangeFake{
		data:     data,
		gates:    make(map[int64]chan struct{}),
		outcomes: make(map[int64]error),
		entered:  make(chan int64, 64),
	}
}

func (f *gatedRangeFake) ref() tgclient.DocumentRef {
	return tgclient.DocumentRef{
		Peer:  tgclient.InputPeer{ChannelID: 42},
		MsgID: 99,
		Size:  int64(len(f.data)),
	}
}

func (f *gatedRangeFake) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	return f.ref(), nil
}

func (f *gatedRangeFake) gateLocked(offset int64) chan struct{} {
	gate, ok := f.gates[offset]
	if !ok {
		gate = make(chan struct{})
		f.gates[offset] = gate
	}
	return gate
}
func (f *gatedRangeFake) release(offset int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gate := f.gateLocked(offset)
	select {
	case <-gate:
	default:
		close(gate)
	}
}

func (f *gatedRangeFake) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if err := validateNormalizedRange(offset, len(dst)); err != nil {
		return 0, err
	}
	f.mu.Lock()
	gate := f.gateLocked(offset)
	f.mu.Unlock()
	f.entered <- offset

	var err error
	select {
	case <-gate:
	case <-ctx.Done():
		err = ctx.Err()
	}
	f.mu.Lock()
	f.outcomes[offset] = err
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if offset+int64(len(dst)) > int64(len(f.data)) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(dst, f.data[offset:offset+int64(len(dst))]), nil
}
func (f *gatedRangeFake) awaitEntered(t *testing.T, want ...int64) {
	t.Helper()
	pending := make(map[int64]struct{}, len(want))
	for _, offset := range want {
		pending[offset] = struct{}{}
	}
	deadline := time.After(2 * time.Second)
	for len(pending) > 0 {
		select {
		case offset := <-f.entered:
			delete(pending, offset)
		case <-deadline:
			t.Fatalf("requests never started for offsets %v", pending)
		}
	}
}
func (f *gatedRangeFake) awaitOutcome(t *testing.T, offset int64) error {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		err, ok := f.outcomes[offset]
		f.mu.Unlock()
		if ok {
			return err
		}
		if time.Now().After(deadline) {
			t.Fatalf("request at %d never ended", offset)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestRangeReaderKeepsWindowOfBlocksInFlight(t *testing.T) {
	data := testBytes(blockBytes*5 + 1)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{ReadAhead: 3})

	if _, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), openingChunkBytes+128); err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}

	block := int64(blockBytes)
	deadline := time.Now().Add(2 * time.Second)
	for {
		offsets := make(map[int64]bool)
		for _, call := range fake.calls() {
			offsets[call.offset] = true
		}
		if offsets[block] && offsets[2*block] && offsets[3*block] {
			if len(offsets) != 4 || offsets[4*block] {
				t.Fatalf("fetched offsets = %v, want block 0 and a window of exactly three blocks", offsets)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("calls = %+v, want three blocks of read-ahead", fake.calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestRangeReaderSeekCancelsStaleReadAhead(t *testing.T) {
	const window = 4
	block := int64(blockBytes)
	data := testBytes(int(block) * 12)
	fake := newGatedRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{ReadAhead: window})
	ref := fake.ref()
	fake.release(0)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), openingChunkBytes); err != nil {
		t.Fatalf("read block 0: %v", err)
	}
	fake.awaitEntered(t, block, 2*block, 3*block, 4*block)
	fake.release(6 * block)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), 6*block+64); err != nil {
		t.Fatalf("read block 6: %v", err)
	}
	for _, offset := range []int64{block, 2 * block, 3 * block, 4 * block} {
		if err := fake.awaitOutcome(t, offset); !errors.Is(err, context.Canceled) {
			t.Fatalf("stale fetch at %d ended with %v, want cancellation", offset, err)
		}
	}
	fake.awaitEntered(t, 7*block, 8*block, 9*block, 10*block)
}
func TestRangeReaderTailReadKeepsHeadReadAhead(t *testing.T) {
	block := int64(blockBytes)
	data := testBytes(int(block) * 12)
	fake := newGatedRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{ReadAhead: 2})
	ref := fake.ref()

	fake.release(0)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), openingChunkBytes); err != nil {
		t.Fatalf("read block 0: %v", err)
	}
	fake.awaitEntered(t, block, 2*block)

	fake.release(11 * block)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), 11*block); err != nil {
		t.Fatalf("read last block: %v", err)
	}
	fake.release(block)
	fake.release(2 * block)
	for _, offset := range []int64{block, 2 * block} {
		if err := fake.awaitOutcome(t, offset); err != nil {
			t.Fatalf("head read-ahead at %d ended with %v, want completion", offset, err)
		}
	}
}

func TestRangeReaderForegroundPromotesQueuedBackgroundFlight(t *testing.T) {
	block := int64(blockBytes)
	data := testBytes(int(block) * 6)
	fake := newGatedRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	for slot := int64(1); slot <= defaultBackgroundSlots; slot++ {
		reader.prefetchBlock(ref, slot*block)
	}
	fake.awaitEntered(t, block, 2*block, 3*block, 4*block)

	queued := 5 * block
	reader.prefetchBlock(ref, queued)
	select {
	case offset := <-fake.entered:
		t.Fatalf("queued background fetch at %d ran without a free lane slot", offset)
	case <-time.After(50 * time.Millisecond):
	}

	buf := make([]byte, 64)
	done := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(context.Background(), ref, buf, queued+64)
		done <- err
	}()
	fake.awaitEntered(t, queued)
	fake.release(queued)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("foreground read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground read waited behind the background queue")
	}
	if !bytes.Equal(buf, data[queued+64:queued+128]) {
		t.Fatal("promoted block bytes mismatch")
	}
}
