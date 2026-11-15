package blockcache

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func refFor(channel, message int64) tgclient.DocumentRef {
	return tgclient.DocumentRef{Peer: tgclient.InputPeer{ChannelID: channel}, MsgID: message}
}

func blockBytes(ref tgclient.DocumentRef, offset int64, length int) []byte {
	out := make([]byte, length)
	seed := byte(ref.Peer.ChannelID*31 + ref.MsgID*7 + offset/BlockAlignment)
	for i := range out {
		out[i] = seed + byte(i)
	}
	return out
}

type fakeOrigin struct {
	calls   atomic.Int64
	read    func(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error)
	mu      sync.Mutex
	offsets []int64
}

func (o *fakeOrigin) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	o.calls.Add(1)
	o.mu.Lock()
	o.offsets = append(o.offsets, offset)
	o.mu.Unlock()
	if o.read != nil {
		return o.read(ctx, ref, offset, dst)
	}
	return copy(dst, blockBytes(ref, offset, len(dst))), nil
}

type testFiles struct {
	fsx.OS
	removeErr func(path string) error
}

func (f testFiles) Remove(name string) error {
	if f.removeErr != nil {
		if err := f.removeErr(name); err != nil {
			return err
		}
	}
	return f.OS.Remove(name)
}

func newTestStore(t *testing.T, dir string, maxBytes int64, maxEntries int, files Files) *store {
	t.Helper()
	if files == nil {
		files = fsx.OS{}
	}
	if maxEntries == 0 {
		maxEntries = defaultMaxEntries
	}
	return newStore(files, dir, maxBytes, maxEntries, discardLogger(), &counters{})
}

func newTestCache(t *testing.T, origin *fakeOrigin, dir string, maxBytes int64) *Cache {
	t.Helper()
	cache, err := New(Dependencies{Origin: origin, Files: fsx.OS{}, Logger: discardLogger(), Dir: dir, MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cache
}

func keyN(n int) blockKey {
	return blockKey{channelID: 1, messageID: int64(n), offset: 0, length: 0}
}

func putBytes(t *testing.T, s *store, n int, data []byte) {
	t.Helper()
	key := keyN(n)
	key.length = len(data)
	if err := s.write(key, data); err != nil {
		t.Fatalf("write %d: %v", n, err)
	}
}

func getBytes(s *store, n, length int) ([]byte, bool) {
	key := keyN(n)
	key.length = length
	dst := make([]byte, length)
	ok := s.read(key, dst)
	return dst, ok
}

func diskBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(dir, func(_ string, de os.DirEntry, walkErr error) error {
		if walkErr != nil || de.IsDir() {
			return nil
		}
		info, err := de.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return total
}

func blockFiles(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, de os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !de.IsDir() && strings.HasSuffix(de.Name(), blockFileSuffix) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return paths
}

type recordingFiles struct {
	Files
	mu     sync.Mutex
	events []string
	onMove func()
}

func (r *recordingFiles) record(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recordingFiles) CreateTemp(dir, pattern string) (fsx.File, error) {
	file, err := r.Files.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &recordingFile{File: file, owner: r}, nil
}

func (r *recordingFiles) Rename(oldpath, newpath string) error {
	r.record("rename")
	if r.onMove != nil {
		r.onMove()
	}
	return r.Files.Rename(oldpath, newpath)
}

type recordingFile struct {
	fsx.File
	owner *recordingFiles
}

func (f *recordingFile) Write(p []byte) (int, error) {
	f.owner.record("write")
	return f.File.Write(p)
}

func (f *recordingFile) Sync() error {
	f.owner.record("sync")
	return f.File.Sync()
}
