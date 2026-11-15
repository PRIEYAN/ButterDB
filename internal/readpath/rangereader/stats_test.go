package rangereader

import (
	"context"
	"testing"
	"time"
)

func TestRangeReaderStatsCountFetchesAndCacheHits(t *testing.T) {
	data := testBytes(blockBytes * 2)
	fake := newStrictRangeFake(data)
	fake.delay = 2 * time.Millisecond
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	for range 2 {
		if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 64), openingChunkBytes); err != nil {
			t.Fatalf("ReadStoredAt: %v", err)
		}
	}

	stats := reader.Stats()
	if stats.Fetched != 1 || stats.CacheHits != 1 || stats.Cancelled != 0 || stats.PeakInFlight != 1 {
		t.Fatalf("stats = %+v, want one fetch, one hit, nothing cancelled, pipeline depth one", stats)
	}
	if stats.BlockP50 < fake.delay || stats.BlockP95 < stats.BlockP50 {
		t.Fatalf("latency percentiles = %v/%v, want at least the fake's %v delay", stats.BlockP50, stats.BlockP95, fake.delay)
	}
}
