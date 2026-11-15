package blockcache

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	good := Dependencies{Origin: &fakeOrigin{}, Files: fsx.OS{}, Logger: discardLogger(), Dir: t.TempDir(), MaxBytes: 1}
	tests := []struct {
		name   string
		mutate func(*Dependencies)
		want   error
	}{
		{"nil origin", func(d *Dependencies) { d.Origin = nil }, errs.ErrMissingDependency},
		{"nil files", func(d *Dependencies) { d.Files = nil }, errs.ErrMissingDependency},
		{"nil logger", func(d *Dependencies) { d.Logger = nil }, errs.ErrMissingDependency},
		{"empty dir", func(d *Dependencies) { d.Dir = "" }, errs.ErrMissingDependency},
		{"negative budget", func(d *Dependencies) { d.MaxBytes = -1 }, nil},
		{"negative entries", func(d *Dependencies) { d.MaxEntries = -1 }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := good
			tt.mutate(&deps)
			_, err := New(deps)
			if err == nil {
				t.Fatal("New succeeded")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestHitServesWithoutOrigin(t *testing.T) {
	origin := &fakeOrigin{}
	cache := newTestCache(t, origin, t.TempDir(), 8<<20)
	ref := refFor(10, 20)
	for i := range 3 {
		dst := make([]byte, BlockMaxBytes)
		n, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst)
		if err != nil || n != len(dst) {
			t.Fatalf("read %d = %d, %v", i, n, err)
		}
		if !bytes.Equal(dst, blockBytes(ref, 0, len(dst))) {
			t.Fatalf("read %d returned wrong bytes", i)
		}
	}
	if got := origin.calls.Load(); got != 1 {
		t.Fatalf("origin calls = %d, want 1", got)
	}
	if stats := cache.Stats(); stats.Hits != 2 || stats.Misses != 1 || stats.FillBytes != BlockMaxBytes {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestKeyDistinguishesChannelMessageOffsetAndLength(t *testing.T) {
	tests := []struct {
		name   string
		ref    tgclient.DocumentRef
		offset int64
		length int
	}{
		{"base", refFor(1, 2), 0, 4096},
		{"other channel", refFor(9, 2), 0, 4096},
		{"other message", refFor(1, 3), 0, 4096},
		{"other offset", refFor(1, 2), 8192, 4096},
		{"other length", refFor(1, 2), 0, 8192},
	}
	origin := &fakeOrigin{}
	cache := newTestCache(t, origin, t.TempDir(), 8<<20)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := make([]byte, tt.length)
			if _, err := cache.ReadDocumentRange(context.Background(), tt.ref, tt.offset, dst); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(dst, blockBytes(tt.ref, tt.offset, tt.length)) {
				t.Fatal("wrong bytes")
			}
		})
	}
	if got := origin.calls.Load(); got != int64(len(tests)) {
		t.Fatalf("origin calls = %d, want %d", got, len(tests))
	}
}

func TestUncacheableShapesPassThroughToOrigin(t *testing.T) {
	tests := []struct {
		name   string
		offset int64
		length int
	}{
		{"unaligned offset", 100, 4096},
		{"over one mebibyte", 0, BlockMaxBytes + 4096},
		{"crosses boundary", BlockMaxBytes - 4096, 8192},
		{"negative offset", -4096, 4096},
	}
	errOrigin := errors.New("origin refused")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := &fakeOrigin{read: func(context.Context, tgclient.DocumentRef, int64, []byte) (int, error) {
				return 0, errOrigin
			}}
			dir := t.TempDir()
			cache := newTestCache(t, origin, dir, 8<<20)
			_, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), tt.offset, make([]byte, tt.length))
			if !errors.Is(err, errOrigin) {
				t.Fatalf("error = %v, want origin error", err)
			}
			if len(blockFiles(t, dir)) != 0 {
				t.Fatal("uncacheable shape was cached")
			}
		})
	}
}

func TestZeroLengthReadDoesNotTouchOrigin(t *testing.T) {
	origin := &fakeOrigin{}
	cache := newTestCache(t, origin, t.TempDir(), 1<<20)
	if n, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, nil); n != 0 || err != nil {
		t.Fatalf("read = %d, %v", n, err)
	}
	if origin.calls.Load() != 0 {
		t.Fatal("origin was called")
	}
}

func TestOriginFailuresAreNotCached(t *testing.T) {
	tests := []struct {
		name string
		read func(context.Context, tgclient.DocumentRef, int64, []byte) (int, error)
	}{
		{"error", func(context.Context, tgclient.DocumentRef, int64, []byte) (int, error) { return 0, errors.New("boom") }},
		{"short read", func(_ context.Context, _ tgclient.DocumentRef, _ int64, dst []byte) (int, error) {
			return len(dst) / 2, nil
		}},
		{"unexpected eof", func(_ context.Context, _ tgclient.DocumentRef, _ int64, dst []byte) (int, error) {
			return 0, io.ErrUnexpectedEOF
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := &fakeOrigin{read: tt.read}
			dir := t.TempDir()
			cache := newTestCache(t, origin, dir, 1<<20)
			if _, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, make([]byte, 4096)); err == nil {
				t.Fatal("read succeeded")
			}
			if len(blockFiles(t, dir)) != 0 {
				t.Fatal("failed read was cached")
			}
		})
	}
}

func TestStoresCiphertextOnly(t *testing.T) {
	plaintext := bytes.Repeat([]byte("PLAINTEXT-MARKER-"), 4096/17+1)[:4096]
	blockCipher, err := aes.NewCipher(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := make([]byte, len(plaintext))
	cipher.NewCTR(blockCipher, make([]byte, aes.BlockSize)).XORKeyStream(sealed, plaintext)
	origin := &fakeOrigin{read: func(_ context.Context, _ tgclient.DocumentRef, _ int64, dst []byte) (int, error) {
		return copy(dst, sealed), nil
	}}
	dir := t.TempDir()
	cache := newTestCache(t, origin, dir, 1<<20)
	dst := make([]byte, len(sealed))
	if _, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, dst); err != nil {
		t.Fatal(err)
	}
	files := blockFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("block files = %d, want 1", len(files))
	}
	onDisk, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, sealed) {
		t.Fatal("cached bytes differ from the stored bytes the origin returned")
	}
	if bytes.Contains(onDisk, []byte("PLAINTEXT-MARKER")) {
		t.Fatal("cache file contains plaintext")
	}
}

func TestTornFileIsMiss(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, path string, size int)
	}{
		{"truncated", func(t *testing.T, path string, size int) {
			if err := os.Truncate(path, int64(size/2)); err != nil {
				t.Fatal(err)
			}
		}},
		{"emptied", func(t *testing.T, path string, size int) {
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"grown", func(t *testing.T, path string, size int) {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if _, err := file.Write([]byte("tail")); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed", func(t *testing.T, path string, size int) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin := &fakeOrigin{}
			dir := t.TempDir()
			cache := newTestCache(t, origin, dir, 1<<20)
			ref := refFor(1, 2)
			dst := make([]byte, 8192)
			if _, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst); err != nil {
				t.Fatal(err)
			}
			files := blockFiles(t, dir)
			if len(files) != 1 {
				t.Fatalf("block files = %d, want 1", len(files))
			}
			tt.damage(t, files[0], len(dst))
			again := make([]byte, len(dst))
			if _, err := cache.ReadDocumentRange(context.Background(), ref, 0, again); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, blockBytes(ref, 0, len(again))) {
				t.Fatal("damaged file produced wrong bytes")
			}
			if got := origin.calls.Load(); got != 2 {
				t.Fatalf("origin calls = %d, want 2 (torn file is a miss)", got)
			}
			after := blockFiles(t, dir)
			if len(after) != 1 {
				t.Fatalf("block files after refill = %d, want 1", len(after))
			}
			if info, err := os.Stat(after[0]); err != nil || info.Size() != int64(len(dst)) {
				t.Fatalf("refilled file size = %v, %v", info, err)
			}
		})
	}
}

func TestBudgetUnderConcurrency(t *testing.T) {
	const budget = 8 * 4096
	origin := &fakeOrigin{}
	dir := t.TempDir()
	tracker := &trackingFiles{Files: fsx.OS{}}
	cache, err := New(Dependencies{Origin: origin, Files: tracker, Logger: discardLogger(), Dir: dir, MaxBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for g := range 16 {
		readers.Go(func() {
			for i := range 40 {
				ref := refFor(1, int64((g*7+i)%24))
				dst := make([]byte, 4096)
				if _, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst); err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if !bytes.Equal(dst, blockBytes(ref, 0, 4096)) {
					t.Error("wrong bytes")
					return
				}
			}
		})
	}
	readers.Wait()
	if peak := tracker.peakBytes(); peak > budget {
		t.Fatalf("peak on-disk bytes %d exceed budget %d", peak, budget)
	}
	if total := diskBytes(t, dir); total > budget {
		t.Fatalf("final disk use %d exceeds budget %d", total, budget)
	}
	if used, _, _ := cache.store.usage(); used > budget {
		t.Fatalf("indexed use %d exceeds budget %d", used, budget)
	}
	if tracker.peakBytes() == 0 {
		t.Fatal("tracker observed no writes")
	}
}

func TestIndexReloadedAtStart(t *testing.T) {
	dir := t.TempDir()
	firstOrigin := &fakeOrigin{}
	first := newTestCache(t, firstOrigin, dir, 1<<20)
	ref := refFor(3, 4)
	for _, offset := range []int64{0, 4096, 8192} {
		if _, err := first.ReadDocumentRange(context.Background(), ref, offset, make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
	}
	secondOrigin := &fakeOrigin{}
	second := newTestCache(t, secondOrigin, dir, 1<<20)
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := second.Stats(); stats.Entries != 3 || stats.UsedBytes != 3*4096 {
		t.Fatalf("reloaded stats = %+v, want 3 entries of 4096 bytes", stats)
	}
	for _, offset := range []int64{0, 4096, 8192} {
		dst := make([]byte, 4096)
		if _, err := second.ReadDocumentRange(context.Background(), ref, offset, dst); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(dst, blockBytes(ref, offset, 4096)) {
			t.Fatalf("reloaded block at %d has wrong bytes", offset)
		}
	}
	if secondOrigin.calls.Load() != 0 {
		t.Fatalf("origin calls after reload = %d, want 0", secondOrigin.calls.Load())
	}
}

func TestIndexEntryAppearsOnlyAfterSyncAndRename(t *testing.T) {
	dir := t.TempDir()
	recorder := &recordingFiles{Files: fsx.OS{}}
	cache, err := New(Dependencies{Origin: &fakeOrigin{}, Files: recorder, Logger: discardLogger(), Dir: dir, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var indexedAtRename int
	recorder.onMove = func() {
		cache.store.mu.Lock()
		indexedAtRename = len(cache.store.entries)
		cache.store.mu.Unlock()
	}
	if _, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	if indexedAtRename != 0 {
		t.Fatalf("index held %d entries before the rename", indexedAtRename)
	}
	recorder.mu.Lock()
	events := append([]string(nil), recorder.events...)
	recorder.mu.Unlock()
	want := []string{"write", "sync", "rename"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	if stats := cache.Stats(); stats.Entries != 1 {
		t.Fatalf("entries after read = %d, want 1", stats.Entries)
	}
}

func TestFailedWriteLeavesNoIndexEntry(t *testing.T) {
	dir := t.TempDir()
	failing := failingRename{Files: fsx.OS{}}
	cache, err := New(Dependencies{Origin: &fakeOrigin{}, Files: failing, Logger: discardLogger(), Dir: dir, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ref := refFor(1, 2)
	dst := make([]byte, 4096)
	if _, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst); err != nil {
		t.Fatalf("read must succeed when caching fails: %v", err)
	}
	if !bytes.Equal(dst, blockBytes(ref, 0, 4096)) {
		t.Fatal("wrong bytes")
	}
	if stats := cache.Stats(); stats.Entries != 0 || stats.UsedBytes != 0 {
		t.Fatalf("stats after failed write = %+v", stats)
	}
	if diskBytes(t, dir) != 0 {
		t.Fatal("failed write left bytes on disk")
	}
}

type failingRename struct{ Files }

func (failingRename) Rename(string, string) error { return errors.New("rename refused") }

func TestConcurrentMissesShareOneOriginRead(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	origin := &fakeOrigin{read: func(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return copy(dst, blockBytes(ref, offset, len(dst))), nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}}
	cache := newTestCache(t, origin, t.TempDir(), 1<<20)
	ref := refFor(1, 2)
	const readers = 8
	errsCh := make(chan error, readers)
	var wait sync.WaitGroup
	for range readers {
		wait.Go(func() {
			dst := make([]byte, 4096)
			_, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst)
			if err == nil && !bytes.Equal(dst, blockBytes(ref, 0, 4096)) {
				err = errors.New("wrong bytes")
			}
			errsCh <- err
		})
	}
	<-started
	waitFor(t, func() bool { return cache.flights.waiters(keyFor(ref, 0, 4096)) == readers })
	close(release)
	wait.Wait()
	close(errsCh)
	for err := range errsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := origin.calls.Load(); got != 1 {
		t.Fatalf("origin calls = %d, want 1", got)
	}
}

func TestCanceledWaiterKeepsLoadAliveForOthers(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	origin := &fakeOrigin{read: func(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return copy(dst, blockBytes(ref, offset, len(dst))), nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}}
	cache := newTestCache(t, origin, t.TempDir(), 1<<20)
	ref := refFor(1, 2)
	key := keyFor(ref, 0, 4096)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := cache.ReadDocumentRange(leaderCtx, ref, 0, make([]byte, 4096))
		leaderErr <- err
	}()
	<-started
	followerDst := make([]byte, 4096)
	followerErr := make(chan error, 1)
	go func() {
		_, err := cache.ReadDocumentRange(context.Background(), ref, 0, followerDst)
		followerErr <- err
	}()
	waitFor(t, func() bool { return cache.flights.waiters(key) == 2 })
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reader error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-followerErr; err != nil {
		t.Fatalf("remaining reader error = %v", err)
	}
	if !bytes.Equal(followerDst, blockBytes(ref, 0, 4096)) {
		t.Fatal("remaining reader got wrong bytes")
	}
	if got := origin.calls.Load(); got != 1 {
		t.Fatalf("origin calls = %d, want 1", got)
	}
}

func TestAbandonedLoadIsCanceledAndNotCached(t *testing.T) {
	started := make(chan struct{})
	loadCanceled := make(chan struct{})
	origin := &fakeOrigin{read: func(ctx context.Context, _ tgclient.DocumentRef, _ int64, _ []byte) (int, error) {
		close(started)
		<-ctx.Done()
		close(loadCanceled)
		return 0, ctx.Err()
	}}
	dir := t.TempDir()
	cache := newTestCache(t, origin, dir, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := cache.ReadDocumentRange(ctx, refFor(1, 2), 0, make([]byte, 4096))
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	select {
	case <-loadCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned load was not canceled")
	}
	if len(blockFiles(t, dir)) != 0 {
		t.Fatal("abandoned load was cached")
	}
	if cache.flights.waiters(keyFor(refFor(1, 2), 0, 4096)) != 0 {
		t.Fatal("flight still registered")
	}
}

func TestWarmPopulatesBlockForLaterRead(t *testing.T) {
	origin := &fakeOrigin{}
	cache := newTestCache(t, origin, t.TempDir(), 1<<20)
	ref := refFor(5, 6)
	block := blockBytes(ref, 0, 4096)
	if err := cache.Warm(ref, 0, block); err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, 4096)
	if _, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dst, block) || origin.calls.Load() != 0 {
		t.Fatalf("warm block not served from cache, origin calls = %d", origin.calls.Load())
	}
	if err := cache.Warm(ref, 1, block); err != nil {
		t.Fatalf("unaligned warm must be ignored: %v", err)
	}
}

func TestClearEmptiesCache(t *testing.T) {
	origin := &fakeOrigin{}
	dir := t.TempDir()
	cache := newTestCache(t, origin, dir, 1<<20)
	if _, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := cache.Stats(); stats.Entries != 0 || stats.UsedBytes != 0 {
		t.Fatalf("stats after clear = %+v", stats)
	}
	if len(blockFiles(t, dir)) != 0 {
		t.Fatal("clear left block files")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCloseCancelsActiveLoadsAndRejectsNewReads(t *testing.T) {
	started := make(chan struct{})
	origin := &fakeOrigin{read: func(ctx context.Context, _ tgclient.DocumentRef, _ int64, _ []byte) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	cache := newTestCache(t, origin, t.TempDir(), 1<<20)
	result := make(chan error, 1)
	go func() {
		_, err := cache.ReadDocumentRange(context.Background(), refFor(1, 2), 0, make([]byte, 4096))
		result <- err
	}()
	<-started
	if err := cache.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight read error = %v, want context.Canceled", err)
	}
	if _, err := cache.ReadDocumentRange(context.Background(), refFor(1, 3), 0, make([]byte, 4096)); !errors.Is(err, errClosed) {
		t.Fatalf("read after close error = %v, want errClosed", err)
	}
}
