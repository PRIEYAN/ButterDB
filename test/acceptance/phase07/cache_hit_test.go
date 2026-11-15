package phase07

import (
	"bytes"
	"context"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
)

func readWhole(t *testing.T, opened objectreader.Opened, size int64, step int) []byte {
	t.Helper()
	out := make([]byte, 0, size)
	for offset := int64(0); offset < size; offset += int64(step) {
		dst := make([]byte, min(int64(step), size-offset))
		n, err := opened.ReadAt(context.Background(), dst, offset)
		if err != nil && int64(n) != int64(len(dst)) {
			t.Fatalf("ReadAt(%d) = %d, %v", offset, n, err)
		}
		out = append(out, dst[:n]...)
	}
	return out
}

func TestCacheHitMakesZeroTelegramCalls(t *testing.T) {
	cases := []struct {
		name       string
		encryption string
		size       int
	}{
		{name: "plain object", encryption: "none", size: 600 * 1024},
		{name: "encrypted object", encryption: "tde1", size: 600 * 1024},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.createBucket("photos", tt.encryption)
			plain := randomBytes("cache body "+tt.name, tt.size)
			w.mustPut("photos", "a.bin", plain)
			cacheDir := t.TempDir()

			first := w.newRead(cacheDir, noReadAhead())
			meta, opened, err := first.Open(context.Background(), "photos", "a.bin")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := readWhole(t, opened, meta.PlaintextSize, 100000); !bytes.Equal(got, plain) {
				t.Fatal("cold read returned different bytes")
			}
			_, coldReads := w.origin.calls()
			if coldReads == 0 {
				t.Fatal("cold read made no Telegram range reads")
			}
			if first.Cache.Stats().UsedBytes == 0 {
				t.Fatal("cold read left the block cache empty")
			}

			t.Run("the same reader serves a repeat read with no Telegram call", func(t *testing.T) {
				resolvesBefore, readsBefore := w.origin.calls()
				clientReadsBefore := w.client.ReadCount()
				again := readWhole(t, opened, meta.PlaintextSize, 100000)
				if !bytes.Equal(again, plain) {
					t.Fatal("repeat read returned different bytes")
				}
				resolvesAfter, readsAfter := w.origin.calls()
				if resolvesAfter != resolvesBefore || readsAfter != readsBefore || w.client.ReadCount() != clientReadsBefore {
					t.Fatalf("repeat read made Telegram calls: resolves %d->%d reads %d->%d", resolvesBefore, resolvesAfter, readsBefore, readsAfter)
				}
			})

			if err := opened.Close(); err != nil {
				t.Fatalf("close opened: %v", err)
			}
			w.closeRead(first)

			t.Run("a restarted reader serves from the disk cache with no range read", func(t *testing.T) {
				clientReadsBefore := w.client.ReadCount()
				second := w.newRead(cacheDir, noReadAhead())
				t.Cleanup(func() { _ = second.Close(context.Background()) })
				if second.Cache.Stats().Entries == 0 {
					t.Fatal("block cache index was not reloaded from disk")
				}
				_, reopened, err := second.Open(context.Background(), "photos", "a.bin")
				if err != nil {
					t.Fatalf("reopen: %v", err)
				}
				defer reopened.Close()
				if got := readWhole(t, reopened, meta.PlaintextSize, 100000); !bytes.Equal(got, plain) {
					t.Fatal("disk cache read returned different bytes")
				}
				if got := w.client.ReadCount() - clientReadsBefore; got != 0 {
					t.Fatalf("disk cache hit made %d Telegram range reads, want 0", got)
				}
				if second.Cache.Stats().Hits == 0 {
					t.Fatal("block cache recorded no hits")
				}
			})

			t.Run("an overwrite is never served from the old body", func(t *testing.T) {
				newer := randomBytes("overwritten "+tt.name, tt.size)
				w.mustPut("photos", "a.bin", newer)
				third := w.newRead(cacheDir, noReadAhead())
				t.Cleanup(func() { _ = third.Close(context.Background()) })
				got, reopened, err := third.Open(context.Background(), "photos", "a.bin")
				if err != nil {
					t.Fatalf("open after overwrite: %v", err)
				}
				defer reopened.Close()
				if got.PutMsgID == meta.PutMsgID {
					t.Fatalf("overwrite kept put %d", got.PutMsgID)
				}
				if body := readWhole(t, reopened, got.PlaintextSize, 100000); !bytes.Equal(body, newer) {
					t.Fatal("read after overwrite returned the old object")
				}
			})
		})
	}
}
