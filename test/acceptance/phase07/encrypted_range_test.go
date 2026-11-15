package phase07

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func TestEncryptedRangeCrossingChunksReturnsExactBytes(t *testing.T) {
	w := newWorld(t)
	info := w.createBucket("vault", "tde1")
	plain := randomBytes("encrypted plaintext", 2*1024*1024+format.ChunkSize+777)
	meta := w.mustPut("vault", "big.bin", plain)
	if !meta.Encrypted || meta.PlaintextSize != int64(len(plain)) || meta.StoredSize != format.CiphertextSize(int64(len(plain))) {
		t.Fatalf("meta = %+v", meta)
	}
	cacheDir := t.TempDir()
	read := w.newRead(cacheDir, noReadAhead())
	t.Cleanup(func() { _ = read.Close(context.Background()) })

	size := int64(len(plain))
	chunk := int64(format.ChunkSize)
	tests := []struct {
		name   string
		offset int64
		length int
	}{
		{name: "two bytes across the first chunk boundary", offset: chunk - 1, length: 2},
		{name: "twenty bytes across the first chunk boundary", offset: chunk - 10, length: 20},
		{name: "three whole chunks from a chunk start", offset: chunk, length: int(3 * chunk)},
		{name: "unaligned span over many chunks", offset: 12345, length: 300000},
		{name: "span across a one MiB block boundary", offset: 1<<20 - 100, length: 200},
		{name: "span across block and chunk boundaries", offset: 1<<20 - chunk/2, length: int(2 * chunk)},
		{name: "last bytes of the object", offset: size - 50, length: 50},
		{name: "whole object", offset: 0, length: len(plain)},
	}
	_, opened, err := read.Open(context.Background(), "vault", "big.bin")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := make([]byte, tt.length)
			n, err := opened.ReadAt(context.Background(), dst, tt.offset)
			if err != nil && !(errors.Is(err, io.EOF) && n == tt.length) {
				t.Fatalf("ReadAt(%d, %d) = %d, %v", tt.offset, tt.length, n, err)
			}
			if n != tt.length || !bytes.Equal(dst, plain[tt.offset:tt.offset+int64(tt.length)]) {
				t.Fatalf("ReadAt(%d, %d) returned %d bytes that differ from the plaintext", tt.offset, tt.length, n)
			}
		})
	}

	t.Run("a read that runs past the end returns the tail and EOF", func(t *testing.T) {
		dst := make([]byte, 100)
		n, err := opened.ReadAt(context.Background(), dst, size-30)
		if n != 30 || !errors.Is(err, io.EOF) || !bytes.Equal(dst[:n], plain[size-30:]) {
			t.Fatalf("ReadAt past end = %d, %v", n, err)
		}
	})

	t.Run("the disk cache holds ciphertext only", func(t *testing.T) {
		probe := plain[500000:500064]
		files := 0
		err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			files++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, probe) {
				t.Fatalf("cache file %s contains plaintext", path)
			}
			return nil
		})
		if err != nil || files == 0 {
			t.Fatalf("walk cache = %d files, %v; want cached blocks", files, err)
		}
		if used := read.Cache.Stats().UsedBytes; used == 0 {
			t.Fatal("block cache holds no bytes after encrypted reads")
		}
	})

	t.Run("the object resolves from the projection to the pinned version", func(t *testing.T) {
		got, _, err := read.Open(context.Background(), "vault", "big.bin")
		if err != nil {
			t.Fatalf("second open: %v", err)
		}
		if got.PutMsgID != meta.PutMsgID || got.ChannelID != info.ChannelID {
			t.Fatalf("opened meta = %+v, want put %d in channel %d", got, meta.PutMsgID, info.ChannelID)
		}
	})

	t.Run("a missing key and a missing bucket map to their sentinels", func(t *testing.T) {
		if _, _, err := read.Open(context.Background(), "vault", "absent"); !errors.Is(err, errs.ErrNoSuchKey) {
			t.Fatalf("missing key = %v, want ErrNoSuchKey", err)
		}
		if _, _, err := read.Open(context.Background(), "nobucket", "big.bin"); !errors.Is(err, errs.ErrNoSuchBucket) {
			t.Fatalf("missing bucket = %v, want ErrNoSuchBucket", err)
		}
	})
}
