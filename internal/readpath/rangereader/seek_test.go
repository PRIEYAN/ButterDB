package rangereader

import (
	"bytes"
	"context"
	"errors"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"sync"
	"testing"
	"time"
)

func TestSeekCancelsReadAhead(t *testing.T) {
	const window = 4
	block := int64(blockBytes)
	data := testBytes(int(block) * 16)
	fake := newGatedRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{ReadAhead: window})
	ref := fake.ref()

	fake.release(0)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), openingChunkBytes); err != nil {
		t.Fatalf("read block 0: %v", err)
	}
	fake.awaitEntered(t, block, 2*block, 3*block, 4*block)

	claimedBuf := make([]byte, 64)
	claimedDone := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(context.Background(), ref, claimedBuf, 4*block+64)
		claimedDone <- err
	}()
	waitForClaim(t, reader, ref, 4*block)

	fake.release(8 * block)
	if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), 8*block+64); err != nil {
		t.Fatalf("read block 8: %v", err)
	}
	for _, offset := range []int64{block, 2 * block, 3 * block} {
		if err := fake.awaitOutcome(t, offset); !errors.Is(err, context.Canceled) {
			t.Fatalf("unclaimed read-ahead at %d ended with %v, want cancellation", offset, err)
		}
	}
	fake.awaitEntered(t, 8*block)
	entered := fake.awaitAnyEntered(t, 3)
	seen := map[int64]bool{}
	for _, offset := range entered {
		if offset < 9*block || offset > 12*block || seen[offset] {
			t.Fatalf("read-ahead window offsets = %v, want distinct blocks 9 to 12", entered)
		}
		seen[offset] = true
	}

	fake.release(4 * block)
	select {
	case err := <-claimedDone:
		if err != nil {
			t.Fatalf("claimed read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claimed read never finished")
	}
	if !bytes.Equal(claimedBuf, data[4*block+64:4*block+128]) {
		t.Fatal("claimed block bytes mismatch")
	}
	if err := fake.awaitOutcome(t, 4*block); err != nil {
		t.Fatalf("claimed flight ended with %v, want completion", err)
	}
}

func waitForClaim(t *testing.T, reader *RangeReader, ref tgclient.DocumentRef, start int64) {
	t.Helper()
	key := blockKey(ref, start)
	deadline := time.Now().Add(2 * time.Second)
	for {
		reader.mu.Lock()
		f, ok := reader.flights[key]
		claimed := ok && f.isClaimed()
		reader.mu.Unlock()
		if claimed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("flight at %d was never claimed", start)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCoalescedFlightSingleRPC(t *testing.T) {
	block := int64(blockBytes)
	data := testBytes(int(block) * 3)
	fake := newGatedRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	reader.prefetchBlock(ref, block)
	fake.awaitEntered(t, block)

	const readers = 8
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := range readers {
		wg.Go(func() {
			buf := make([]byte, 32)
			off := block + int64(i)*64
			if _, err := reader.ReadStoredAt(context.Background(), ref, buf, off); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(buf, data[off:off+32]) {
				errs <- errors.New("bytes mismatch")
			}
		})
	}
	for range 4 {
		reader.prefetchBlock(ref, block)
	}
	select {
	case offset := <-fake.entered:
		t.Fatalf("second RPC started for offset %d", offset)
	case <-time.After(50 * time.Millisecond):
	}

	fake.release(block)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	select {
	case offset := <-fake.entered:
		t.Fatalf("extra RPC after release for offset %d", offset)
	default:
	}
}

func (f *gatedRangeFake) awaitAnyEntered(t *testing.T, count int) []int64 {
	t.Helper()
	var offsets []int64
	deadline := time.After(2 * time.Second)
	for len(offsets) < count {
		select {
		case offset := <-f.entered:
			offsets = append(offsets, offset)
		case <-deadline:
			t.Fatalf("only %d of %d requests started: %v", len(offsets), count, offsets)
		}
	}
	return offsets
}
