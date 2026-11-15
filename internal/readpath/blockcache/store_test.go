package blockcache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func TestStoreRejectsEntriesThatCannotFit(t *testing.T) {
	s := newTestStore(t, t.TempDir(), 4, 0, nil)
	key := keyN(1)
	key.length = 5
	if err := s.write(key, []byte("12345")); !errors.Is(err, errEntryTooLarge) {
		t.Fatalf("write oversized error = %v, want errEntryTooLarge", err)
	}
	if _, ok := getBytes(s, 1, 5); ok {
		t.Fatal("oversized entry was stored")
	}

	disabled := newTestStore(t, t.TempDir(), 0, 0, nil)
	key.length = 1
	if err := disabled.write(key, []byte("1")); !errors.Is(err, errCacheDisabled) {
		t.Fatalf("write with zero budget error = %v, want errCacheDisabled", err)
	}
}

func TestStoreConcurrentWritesNeverExceedBudgetIncludingTemps(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, 64, 0, nil)
	const writers = 16
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := range writers {
		wait.Go(func() {
			<-start
			key := keyN(i)
			key.length = 32
			err := s.write(key, bytes.Repeat([]byte{byte(i)}, 32))
			if err != nil && !errors.Is(err, errCacheFull) {
				t.Errorf("write: %v", err)
			}
		})
	}
	close(start)
	wait.Wait()
	if total := diskBytes(t, dir); total > 64 {
		t.Fatalf("cache files use %d bytes, want <= 64", total)
	}
}

func TestStoreEvictsAnotherEntryWhenOldestDeleteFails(t *testing.T) {
	dir := t.TempDir()
	var oldPath string
	files := testFiles{removeErr: func(path string) error {
		if oldPath != "" && path == oldPath {
			return errors.New("in use")
		}
		return nil
	}}
	s := newTestStore(t, dir, 10, 0, files)
	putBytes(t, s, 1, []byte("12345"))
	putBytes(t, s, 2, []byte("67890"))
	oldKey := keyN(1)
	oldKey.length = 5
	oldPath = filepath.Join(dir, oldKey.relativeName())
	putBytes(t, s, 3, []byte("abcde"))
	if used, _, _ := s.usage(); used > 10 {
		t.Fatalf("used = %d, want <= 10", used)
	}
	if _, ok := getBytes(s, 2, 5); ok {
		t.Fatal("removable entry was not evicted")
	}
}

func TestStoreUsageAndClear(t *testing.T) {
	s := newTestStore(t, t.TempDir(), 100, 0, nil)
	putBytes(t, s, 1, []byte("value"))
	if used, budget, entries := s.usage(); used != 5 || budget != 100 || entries != 1 {
		t.Fatalf("usage = (%d, %d, %d), want (5, 100, 1)", used, budget, entries)
	}
	if err := s.clear(context.Background()); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if used, _, entries := s.usage(); used != 0 || entries != 0 {
		t.Fatalf("usage after clear = (%d, %d), want (0, 0)", used, entries)
	}
	if _, ok := getBytes(s, 1, 5); ok {
		t.Fatal("cleared entry was returned")
	}
}

func TestStoreStartupBoundsUndeletableOverflowMetadata(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.blk", "b.blk", "c.blk", "d.blk", "e.blk"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), fileMode); err != nil {
			t.Fatal(err)
		}
	}
	files := testFiles{removeErr: func(string) error { return errors.New("read-only") }}
	s := newTestStore(t, dir, 100, 2, files)
	s.init()
	s.mu.Lock()
	indexed, overflow := len(s.entries), s.overflowEntries
	s.mu.Unlock()
	if indexed > 2 {
		t.Fatalf("indexed entries = %d, want <= 2", indexed)
	}
	if overflow != 3 {
		t.Fatalf("overflow entries = %d, want 3", overflow)
	}
	if used, _, entries := s.usage(); used != 5 || entries != 5 {
		t.Fatalf("usage = (%d bytes, %d entries), want (5, 5)", used, entries)
	}
	key := keyN(9)
	key.length = 1
	if err := s.write(key, []byte("x")); !errors.Is(err, errCacheFull) {
		t.Fatalf("write with undeletable overflow = %v, want errCacheFull", err)
	}
}

func TestStorePutGetRoundTrip(t *testing.T) {
	s := newTestStore(t, t.TempDir(), 1<<20, 0, nil)
	want := []byte("stored-block-bytes")
	putBytes(t, s, 1, want)
	got, ok := getBytes(s, 1, len(want))
	if !ok {
		t.Fatal("miss after write")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStoreConcurrentSameKeyWritesKeepAccurateAccounting(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, 1<<20, 0, nil)
	const writers = 64
	start := make(chan struct{})
	var wait sync.WaitGroup
	data := bytes.Repeat([]byte{7}, 1000)
	key := keyN(1)
	key.length = len(data)
	for range writers {
		wait.Go(func() {
			<-start
			if err := s.write(key, data); err != nil {
				t.Errorf("write: %v", err)
			}
		})
	}
	close(start)
	wait.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used != int64(len(data)) || len(s.entries) != 1 || s.pending != 0 {
		t.Fatalf("used=%d entries=%d pending=%d, want %d 1 0", s.used, len(s.entries), s.pending, len(data))
	}
	if disk := diskBytes(t, dir); disk != int64(len(data)) {
		t.Fatalf("disk=%d, want %d", disk, len(data))
	}
}

func TestStoreMissReturnsFalse(t *testing.T) {
	s := newTestStore(t, t.TempDir(), 1<<20, 0, nil)
	if _, ok := getBytes(s, 1, 4); ok {
		t.Fatal("expected miss")
	}
}

func TestDisabledCacheIsPassThrough(t *testing.T) {
	origin := &fakeOrigin{}
	dir := t.TempDir()
	cache := newTestCache(t, origin, dir, 0)
	ref := refFor(1, 2)
	for range 2 {
		dst := make([]byte, 4096)
		if n, err := cache.ReadDocumentRange(context.Background(), ref, 0, dst); err != nil || n != 4096 {
			t.Fatalf("read = %d, %v", n, err)
		}
	}
	if got := origin.calls.Load(); got != 2 {
		t.Fatalf("origin calls = %d, want 2", got)
	}
	if len(blockFiles(t, dir)) != 0 {
		t.Fatal("disabled cache wrote a block")
	}
}

func TestStoreEvictsToStayWithinBudget(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, 250, 0, nil)
	val := bytes.Repeat([]byte{1}, 100)
	for n := 1; n <= 3; n++ {
		putBytes(t, s, n, val)
	}
	s.mu.Lock()
	used, count := s.used, len(s.entries)
	s.mu.Unlock()
	if used > 250 {
		t.Fatalf("used = %d, want <= 250", used)
	}
	if count != 2 {
		t.Fatalf("entries = %d, want 2", count)
	}
	if files := blockFiles(t, dir); len(files) != 2 {
		t.Fatalf("on-disk blocks = %d, want 2", len(files))
	}
}

func TestStoreEvictsLeastRecentlyUsed(t *testing.T) {
	s := newTestStore(t, t.TempDir(), 250, 0, nil)
	val := bytes.Repeat([]byte{2}, 100)
	putBytes(t, s, 1, val)
	putBytes(t, s, 2, val)
	if _, ok := getBytes(s, 1, 100); !ok {
		t.Fatal("get 1 miss")
	}
	putBytes(t, s, 3, val)
	if _, ok := getBytes(s, 2, 100); ok {
		t.Fatal("entry 2 should have been evicted as least recently used")
	}
	if _, ok := getBytes(s, 1, 100); !ok {
		t.Fatal("entry 1 should survive")
	}
	if _, ok := getBytes(s, 3, 100); !ok {
		t.Fatal("entry 3 should survive")
	}
}

func TestStoreReloadsIndexFromDisk(t *testing.T) {
	dir := t.TempDir()
	first := newTestStore(t, dir, 1<<20, 0, nil)
	putBytes(t, first, 1, []byte("survives-restart"))
	second := newTestStore(t, dir, 1<<20, 0, nil)
	got, ok := getBytes(second, 1, len("survives-restart"))
	if !ok || string(got) != "survives-restart" {
		t.Fatalf("reloaded get = %q, %v", got, ok)
	}
}

func TestStoreCleansStrayTempFiles(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, tempFilePrefix+"leftover")
	if err := os.WriteFile(stray, []byte("interrupted"), fileMode); err != nil {
		t.Fatal(err)
	}
	newTestStore(t, dir, 1<<20, 0, nil).init()
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray temp file not cleaned: %v", err)
	}
}

func TestStoreUsesCollisionResistantShardedPaths(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, 1<<20, 0, nil)
	putBytes(t, s, 1, []byte("one"))
	putBytes(t, s, 2, []byte("two"))
	for n, want := range map[int]string{1: "one", 2: "two"} {
		got, ok := getBytes(s, n, len(want))
		if !ok || string(got) != want {
			t.Fatalf("get %d = %q, %v want %q", n, got, ok, want)
		}
		key := keyN(n)
		key.length = len(want)
		parts := strings.Split(filepath.ToSlash(key.relativeName()), "/")
		if len(parts) != 3 || len(parts[0]) != 2 || len(parts[1]) != 2 {
			t.Fatalf("relative path %q is not two-level sharded", key.relativeName())
		}
		if _, err := os.Stat(filepath.Join(dir, key.relativeName())); err != nil {
			t.Fatalf("stat sharded entry: %v", err)
		}
	}
	if keyN(1).relativeName() == keyN(2).relativeName() {
		t.Fatal("distinct keys share a path")
	}
}

func TestStoreBoundsIndexedEntriesAndReloadsShards(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, 1<<20, 2, nil)
	for n := 1; n <= 3; n++ {
		putBytes(t, s, n, []byte("blk"))
	}
	if _, ok := getBytes(s, 1, 3); ok {
		t.Fatal("oldest entry survived the entry-count bound")
	}
	reloaded := newTestStore(t, dir, 1<<20, 2, nil)
	for _, n := range []int{2, 3} {
		if got, ok := getBytes(reloaded, n, 3); !ok || string(got) != "blk" {
			t.Fatalf("reloaded %d = %q, %v", n, got, ok)
		}
	}
}

func TestStoreShardsAndFilesKeepPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blocks")
	s := newTestStore(t, dir, 1<<20, 0, nil)
	putBytes(t, s, 1, []byte("private"))
	key := keyN(1)
	key.length = len("private")
	parts := strings.Split(filepath.ToSlash(key.relativeName()), "/")
	for _, path := range []string{dir, filepath.Join(dir, parts[0]), filepath.Join(dir, parts[0], parts[1])} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != dirMode {
			t.Fatalf("directory %s mode = %o, want %o", path, info.Mode().Perm(), dirMode)
		}
	}
	info, err := os.Stat(filepath.Join(dir, key.relativeName()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fileMode {
		t.Fatalf("file mode = %o, want %o", info.Mode().Perm(), fileMode)
	}
}

func TestStoreWorksThroughRootedFS(t *testing.T) {
	root, err := fsx.NewRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	s := newTestStore(t, filepath.Join(root.Dir(), "blocks"), 1<<20, 0, root)
	putBytes(t, s, 1, []byte("rooted"))
	if got, ok := getBytes(s, 1, 6); !ok || string(got) != "rooted" {
		t.Fatalf("rooted get = %q, %v", got, ok)
	}
	reloaded := newTestStore(t, filepath.Join(root.Dir(), "blocks"), 1<<20, 0, root)
	if got, ok := getBytes(reloaded, 1, 6); !ok || string(got) != "rooted" {
		t.Fatalf("rooted reload = %q, %v", got, ok)
	}
}
