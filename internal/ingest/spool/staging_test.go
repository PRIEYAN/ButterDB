package spool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func newTestSpool(t *testing.T, dir string, budget Budget) *Spool {
	t.Helper()
	s, err := NewWithBudget(dir, fsx.OS{}, clock.NewFake(time.Unix(1700000000, 0)), budget)
	if err != nil {
		t.Fatalf("new spool: %v", err)
	}
	return s
}

func TestDiskStagingStoreStreamsHashesAndUsesPrivatePermissions(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "spool")
	store := newTestSpool(t, root, Budget{MaxBytes: 2 << 20, MaxConcurrent: 2})
	payload := bytes.Repeat([]byte("butter"), 4096)

	if _, err := store.Write(context.Background(), "../../unsafe-job-id", bytes.NewReader(payload), int64(len(payload)), 1<<20); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe job id error = %v, want ErrInvalidRequest", err)
	}
	staged, err := store.Write(context.Background(), "job-1", bytes.NewReader(payload), int64(len(payload)), 1<<20)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = store.Remove("job-1") })

	if staged.Size != int64(len(payload)) || staged.SHA256 != sha256.Sum256(payload) {
		t.Fatalf("spooled metadata = size %d hash %x", staged.Size, staged.SHA256)
	}
	if filepath.Dir(staged.Path) != root || filepath.Base(staged.Path) != "job-1.plain" {
		t.Fatalf("spooled path = %q", staged.Path)
	}

	reader, err := store.Open(staged)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read spooled: read=%v close=%v", err, closeErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("spooled payload differs")
	}

	if runtime.GOOS != "windows" {
		assertFileMode(t, root, 0o700)
		assertFileMode(t, staged.Path, 0o600)
	}
}

func TestDiskStagingStoreEnforcesObjectAndAggregateQuota(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{MaxBytes: 10, MaxConcurrent: 1})

	if _, err := store.Write(context.Background(), "too-big", bytes.NewReader([]byte("123456789")), -1, 8); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("object limit error = %v, want ErrObjectTooLarge", err)
	}
	if entries, err := os.ReadDir(store.dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed write left data: entries=%v err=%v", entries, err)
	}

	if _, err := store.Write(context.Background(), "first", bytes.NewReader([]byte("1234567")), 7, 8); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := store.Write(context.Background(), "second", bytes.NewReader([]byte("1234")), 4, 8); !errors.Is(err, ErrBudgetExhausted) || !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("aggregate limit error = %v, want ErrBudgetExhausted", err)
	}
	if err := store.Remove("first"); err != nil {
		t.Fatalf("remove first: %v", err)
	}
	if _, err := store.Write(context.Background(), "second", bytes.NewReader([]byte("1234")), 4, 8); err != nil {
		t.Fatalf("budget was not released: %v", err)
	}
}

func TestDiskStagingStoreRejectsLengthMismatchAndCleansPartialFile(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{MaxBytes: 1024, MaxConcurrent: 1})

	_, err := store.Write(context.Background(), "wrong-length", bytes.NewReader([]byte("short")), 8, 1024)
	if !errors.Is(err, ErrLengthMismatch) {
		t.Fatalf("write error = %v, want ErrLengthMismatch", err)
	}
	entries, readErr := os.ReadDir(store.dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("length mismatch left data: entries=%v err=%v", entries, readErr)
	}
	if store.UsedBytes() != 0 {
		t.Fatalf("length mismatch retained %d bytes", store.UsedBytes())
	}
}

func TestDiskStagingStoreHonorsCancellationWhileWaitingForSlot(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{MaxBytes: 2048, MaxConcurrent: 1})

	blocking := newBlockingReader()
	firstDone := make(chan error, 1)
	go func() {
		_, writeErr := store.Write(context.Background(), "first", blocking, -1, 1024)
		firstDone <- writeErr
	}()
	blocking.waitUntilRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := store.Write(ctx, "second", bytes.NewReader(nil), -1, 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting write error = %v, want deadline exceeded", err)
	}

	blocking.unblock()
	if err := <-firstDone; err != nil {
		t.Fatalf("first write: %v", err)
	}
}

func TestDiskStagingStoreCountsExistingFilesOnStartup(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.plain"), []byte("1234567"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "crashed.plain.tmp-1"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("seed temp: %v", err)
	}

	store := newTestSpool(t, root, Budget{MaxBytes: 10, MaxConcurrent: 1})
	if _, err := os.Stat(filepath.Join(root, "crashed.plain.tmp-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover temp file survived startup: %v", err)
	}
	if store.UsedBytes() != 7 {
		t.Fatalf("startup used bytes = %d, want 7", store.UsedBytes())
	}
	if _, err := store.Write(context.Background(), "new", bytes.NewReader([]byte("1234")), 4, 10); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("startup budget error = %v, want ErrBudgetExhausted", err)
	}
	if err := store.Remove("existing"); err != nil {
		t.Fatalf("remove existing: %v", err)
	}
	if store.UsedBytes() != 0 {
		t.Fatalf("used bytes after remove = %d", store.UsedBytes())
	}
}

func TestDiskStagingStoreValidatesConfigurationAndPerRequestLimit(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "spool")
	fake := clock.NewFake(time.Unix(1700000000, 0))
	if _, err := New("", fsx.OS{}, fake); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty dir error = %v", err)
	}
	if _, err := New(root, nil, fake); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil fs error = %v", err)
	}
	if _, err := New(root, fsx.OS{}, nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil clock error = %v", err)
	}
	for _, budget := range []Budget{{MaxBytes: -1}, {MaxConcurrent: -1}} {
		if _, err := NewWithBudget(root, fsx.OS{}, fake, budget); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid budget %#v error = %v", budget, err)
		}
	}
	store := newTestSpool(t, root, Budget{MaxBytes: 100, MaxConcurrent: 1})
	if _, err := store.Write(context.Background(), "per-request", bytes.NewReader([]byte("123456")), 6, 5); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("per-request limit error = %v, want ErrObjectTooLarge", err)
	}
	if _, err := store.Write(context.Background(), "invalid-length", bytes.NewReader(nil), -2, 0); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid length error = %v, want ErrInvalidRequest", err)
	}
}

func TestDiskStagingStoreRejectsCollisionCorruptionAndUnsafeReferences(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{MaxBytes: 2048, MaxConcurrent: 1})
	staged, err := store.Write(context.Background(), "collision", bytes.NewReader([]byte("data")), 4, 1024)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.Write(context.Background(), "collision", bytes.NewReader([]byte("data")), 4, 1024); !errors.Is(err, ErrJobFileExists) {
		t.Fatalf("collision error = %v, want ErrJobFileExists", err)
	}

	unsafe := staged
	unsafe.Path = filepath.Join(store.dir, "..", "escape.plain")
	if _, err := store.Open(unsafe); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe open error = %v, want ErrInvalidRequest", err)
	}
	if err := store.Remove("../escape"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe remove error = %v, want ErrInvalidRequest", err)
	}
	wrongPath := staged
	wrongPath.Path = filepath.Join(t.TempDir(), "collision.plain")
	if _, err := store.Open(wrongPath); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mismatched spool path error = %v, want ErrInvalidRequest", err)
	}
	missing := File{Path: filepath.Join(store.dir, "missing.plain"), Size: 1}
	if _, err := store.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing open error = %v, want ErrNotFound", err)
	}
	if err := store.Remove("missing"); err != nil {
		t.Fatalf("remove missing should be idempotent: %v", err)
	}

	if err := os.Truncate(staged.Path, 1); err != nil {
		t.Fatalf("corrupt spool file: %v", err)
	}
	if _, err := store.Open(staged); !errors.Is(err, ErrFileCorrupt) {
		t.Fatalf("corrupt open error = %v, want ErrFileCorrupt", err)
	}
	if err := store.Remove("collision"); err != nil {
		t.Fatalf("remove corrupted file: %v", err)
	}
	if err := store.Remove("collision"); err != nil {
		t.Fatalf("repeat remove: %v", err)
	}
	if store.UsedBytes() != 0 {
		t.Fatalf("used bytes after removals = %d", store.UsedBytes())
	}
}

func TestDiskStagingStoreCleansReaderFailureAndNoProgress(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{MaxBytes: 2048, MaxConcurrent: 1})
	if _, err := store.Write(context.Background(), "reader-error", &errorReader{}, -1, 1024); !errors.Is(err, errSourceFailure) {
		t.Fatalf("reader error = %v, want source failure", err)
	}
	if _, err := store.Write(context.Background(), "no-progress", zeroReader{}, -1, 1024); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("no-progress error = %v, want ErrNoProgress", err)
	}
	if store.UsedBytes() != 0 {
		t.Fatalf("failed readers retained %d bytes", store.UsedBytes())
	}
	if entries, err := os.ReadDir(store.dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed readers left data: entries=%v err=%v", entries, err)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %#o, want %#o", path, got, want)
	}
}

var errSourceFailure = errors.New("source failure")

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type errorReader struct {
	read bool
}

func (r *errorReader) Read(buffer []byte) (int, error) {
	if !r.read {
		r.read = true
		copy(buffer, "partial")
		return len("partial"), errSourceFailure
	}
	return 0, io.EOF
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func newBlockingReader() *blockingReader {
	return &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingReader) Read(_ []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}

func (r *blockingReader) waitUntilRead(t *testing.T) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reader was not started")
	}
}

func (r *blockingReader) unblock() {
	close(r.release)
}
