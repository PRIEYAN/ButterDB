package rangereader

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRangeReaderServesOpeningPrefixBeforeFullBlock(t *testing.T) {
	data := testBytes(blockBytes * 2)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	buf := make([]byte, 64)
	if _, err := reader.ReadStoredAt(context.Background(), ref, buf, 128); err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if !bytes.Equal(buf, data[128:192]) {
		t.Fatal("opening read returned the wrong bytes")
	}

	calls := fake.calls()
	if len(calls) == 0 {
		t.Fatal("no range call was issued")
	}
	if calls[0].offset != 0 || int64(calls[0].length) != openingChunkBytes {
		t.Fatalf("first call = %+v, want the %d-byte opening prefix", calls[0], openingChunkBytes)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range fake.calls() {
			if call.offset == 0 && call.length == blockBytes {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("calls = %+v, want the full block fetched behind the prefix", fake.calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestRangeReaderSkipsOpeningPrefixForSmallFiles(t *testing.T) {
	data := testBytes(int(openingChunkBytes) / 2)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})

	if _, err := reader.ReadStoredAt(context.Background(), fake.ref(), make([]byte, 64), 0); err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	calls := fake.calls()
	if len(calls) != 1 || calls[0].length != len(data) {
		t.Fatalf("calls = %+v, want one fetch of the whole file", calls)
	}
}
