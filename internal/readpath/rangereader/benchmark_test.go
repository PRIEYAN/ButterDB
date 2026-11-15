package rangereader

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type benchmarkRangeClient struct {
	calls atomic.Int64
	bytes atomic.Int64
}

func (*benchmarkRangeClient) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	return tgclient.DocumentRef{MsgID: 1, Size: 128 << 20}, nil
}

func (c *benchmarkRangeClient) ReadDocumentRange(ctx context.Context, _ tgclient.DocumentRef, off int64, dst []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateNormalizedRange(off, len(dst)); err != nil {
		return 0, err
	}
	clear(dst)
	c.calls.Add(1)
	c.bytes.Add(int64(len(dst)))
	return len(dst), nil
}

func BenchmarkRangeReader(b *testing.B) {
	const readSize = 256 << 10
	for _, scenario := range []struct {
		name   string
		stride int64
	}{
		{name: "Sequential", stride: readSize},
		{name: "Seeking", stride: 17 << 20},
		{name: "Cached"},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			client := &benchmarkRangeClient{}
			ref, _ := client.ResolveDocument(b.Context(), tgclient.InputPeer{}, 1)
			reader := startReader(b, client, RangeReaderConfig{MaxCacheBytes: 4 << 20})
			buf := make([]byte, readSize)
			off := int64(1 << 20)
			if _, err := reader.ReadStoredAt(b.Context(), ref, buf, off); err != nil {
				b.Fatal(err)
			}
			client.calls.Store(0)
			client.bytes.Store(0)
			b.ReportAllocs()
			b.SetBytes(readSize)
			for b.Loop() {
				n, err := reader.ReadStoredAt(b.Context(), ref, buf, off)
				if err != nil || n != len(buf) {
					b.Fatalf("read at %d: n=%d, err=%v", off, n, err)
				}
				off = (off+scenario.stride-(1<<20))%(ref.Size-(1<<20)) + (1 << 20)
			}
			client.report(b)
		})
	}
}

func BenchmarkRangeReaderConcurrentCached(b *testing.B) {
	const readSize = 256 << 10
	client := &benchmarkRangeClient{}
	ref, _ := client.ResolveDocument(b.Context(), tgclient.InputPeer{}, 1)
	reader := startReader(b, client, RangeReaderConfig{})
	if _, err := reader.ReadStoredAt(b.Context(), ref, make([]byte, readSize), 1<<20); err != nil {
		b.Fatal(err)
	}
	client.calls.Store(0)
	client.bytes.Store(0)
	b.ReportAllocs()
	b.SetBytes(readSize)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, readSize)
		for pb.Next() {
			n, err := reader.ReadStoredAt(b.Context(), ref, buf, 1<<20)
			if err != nil || n != len(buf) {
				b.Errorf("read: n=%d, err=%v", n, err)
				return
			}
		}
	})
	b.StopTimer()
	client.report(b)
}

func (c *benchmarkRangeClient) report(b *testing.B) {
	b.Helper()
	b.ReportMetric(float64(c.calls.Load())/float64(b.N), "fetches/op")
	b.ReportMetric(float64(c.bytes.Load())/float64(b.N), "transport-B/op")
}
