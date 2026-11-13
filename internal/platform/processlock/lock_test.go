package processlock

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

func newLockPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "butterdbd.lock")
}

func acquire(t *testing.T, path, role string) (*Lock, error) {
	t.Helper()
	return Acquire(path, role, clock.System{})
}

func TestAcquireIgnoresReleasedOwnerMetadata(t *testing.T) {
	path := newLockPath(t)
	metadata, err := json.Marshal(map[string]any{"role": "serve", "pid": os.Getpid(), "started_at": 1, "lock_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	_, active, err := ReadActive(path)
	if err != nil || active {
		t.Fatalf("stale owner active=%v error=%v", active, err)
	}
	lock, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatalf("stale live PID blocked startup: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	})
	info, active, err := ReadActive(path)
	if err != nil || !active || info.PID != os.Getpid() || info.Role != "serve" {
		t.Fatalf("active owner=%+v active=%v error=%v", info, active, err)
	}
}

func TestReleaseKeepsGuardInodeAndCannotReleaseSuccessor(t *testing.T) {
	path := newLockPath(t)
	first, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path + ".guard")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := acquire(t, path, "check")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Release(); err != nil {
			t.Error(err)
		}
	})
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path + ".guard")
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("guard inode was replaced: %v", err)
	}
	if lock, err := acquire(t, path, "serve"); !errors.Is(err, ErrAlreadyRunning) {
		if lock != nil {
			_ = lock.Release()
		}
		t.Fatalf("old release unlocked successor: %v", err)
	}
}

func TestReleaseDeletesNeitherFile(t *testing.T) {
	path := newLockPath(t)
	lock, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".guard"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s removed on release: %v", filepath.Base(name), err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(name), info.Mode().Perm())
		}
	}
}

func TestConcurrentAcquisitionsHaveOneOwner(t *testing.T) {
	path := newLockPath(t)
	start := make(chan struct{})
	results := make(chan *Lock, 16)
	failures := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			lock, err := acquire(t, path, "serve")
			if err != nil {
				failures <- err
				return
			}
			results <- lock
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	winners := 0
	for lock := range results {
		winners++
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	}
	if winners != 1 {
		t.Fatalf("owners=%d, want exactly one", winners)
	}
	for err := range failures {
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("contender error=%v", err)
		}
	}
}

func TestAcquireRejectsSecondBackend(t *testing.T) {
	path := newLockPath(t)

	first, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = acquire(t, path, "rebuild")
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire err = %v, want ErrAlreadyRunning", err)
	}
}

func TestProcessLockSecondInstance(t *testing.T) {
	path := newLockPath(t)
	started := time.Unix(1763000000, 0)
	first, err := Acquire(path, "serve", clock.NewFake(started))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()

	second, err := acquire(t, path, "serve")
	if second != nil {
		_ = second.Release()
		t.Fatal("second instance acquired the lock")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second instance err = %v, want ErrAlreadyRunning", err)
	}
	if text := err.Error(); !strings.Contains(text, "serve") || !strings.Contains(text, "pid") {
		t.Fatalf("error does not name the owner: %q", text)
	}
	info, active, err := ReadActive(path)
	if err != nil || !active {
		t.Fatalf("ReadActive active=%v err=%v", active, err)
	}
	if info.Role != "serve" || info.PID != os.Getpid() || info.StartedAt != started.Unix() || info.LockVersion != 1 {
		t.Fatalf("owner metadata = %+v", info)
	}
}

func TestAcquireRejectsNilClock(t *testing.T) {
	if _, err := Acquire(newLockPath(t), "serve", nil); !errors.Is(err, ErrNilClock) {
		t.Fatalf("err = %v, want ErrNilClock", err)
	}
}

func TestAcquireRecordsClockTimeAndDefaultRole(t *testing.T) {
	path := newLockPath(t)
	started := time.Unix(1700000000, 0)
	lock, err := Acquire(path, "", clock.NewFake(started))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	want := Info{Role: "unknown", PID: os.Getpid(), StartedAt: started.Unix(), LockVersion: 1}
	if got := lock.Info(); got != want {
		t.Fatalf("Info = %+v, want %+v", got, want)
	}
	onDisk, err := Read(path)
	if err != nil || onDisk != want {
		t.Fatalf("Read = %+v err=%v, want %+v", onDisk, err, want)
	}
}

func TestActiveGuardDoesNotDependOnReadableMetadata(t *testing.T) {
	path := newLockPath(t)
	lock, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	})
	for _, remove := range []bool{false, true} {
		if remove {
			err = os.Remove(path)
		} else {
			err = os.WriteFile(path, []byte("broken metadata"), fileMode)
		}
		if err != nil {
			t.Fatal(err)
		}
		info, active, err := ReadActive(path)
		if err != nil || !active || info.Role != "unknown" {
			t.Fatalf("unreadable metadata owner=%+v active=%v error=%v", info, active, err)
		}
		if second, err := acquire(t, path, "rebuild"); !errors.Is(err, ErrAlreadyRunning) {
			if second != nil {
				_ = second.Release()
			}
			t.Fatalf("metadata change bypassed live guard: %v", err)
		}
	}
}

func TestAcquireReclaimsStaleLock(t *testing.T) {
	path := newLockPath(t)
	if err := os.WriteFile(path, []byte(`{"role":"serve","pid":-1,"started_at":1}`), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	lock, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatalf("acquire after stale lock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	if lock.Info().Role != "serve" {
		t.Fatalf("lock role = %q, want serve", lock.Info().Role)
	}
}

func TestUnknownMetadataBlocksStartupWithoutRetainingGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
	}{
		{"damaged record", `{"role":`},
		{"future version", `{"role":"serve","pid":-1,"lock_version":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := newLockPath(t)
			if err := os.WriteFile(path, []byte(tc.record), fileMode); err != nil {
				t.Fatal(err)
			}
			lock, err := acquire(t, path, "serve")
			if lock != nil {
				t.Cleanup(func() { _ = lock.Release() })
			}
			if !errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("unknown ownership allowed startup: %v", err)
			}
			_, active, err := ReadActive(path)
			if err != nil || !active {
				t.Fatalf("unknown ownership active=%v error=%v", active, err)
			}
			record, err := os.ReadFile(path)
			if err != nil || string(record) != tc.record {
				t.Fatalf("ownership record changed: %q error=%v", record, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			recovered, err := acquire(t, path, "rebuild")
			if err != nil {
				t.Fatalf("refused startup retained ownership: %v", err)
			}
			t.Cleanup(func() {
				if err := recovered.Release(); err != nil {
					t.Error(err)
				}
			})
		})
	}
}

func TestMetadataPublicationFailurePreservesExistingRecord(t *testing.T) {
	path := newLockPath(t)
	original := `{"role":"serve","pid":-1,"lock_version":2}`
	if err := os.WriteFile(path, []byte(original), fileMode); err != nil {
		t.Fatal(err)
	}
	if err := writeInfo(path, Info{Role: "rebuild", PID: os.Getpid(), LockVersion: 1}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("publication replaced unknown owner: %v", err)
	}
	record, err := os.ReadFile(path)
	if err != nil || string(record) != original {
		t.Fatalf("existing record changed: %q error=%v", record, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".butterdbd-lock-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary metadata leaked: %v error=%v", leftovers, err)
	}
}

func TestReleaseOnNilAndRepeatedLockIsSafe(t *testing.T) {
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatal(err)
	}
	if got := nilLock.Info(); got != (Info{}) {
		t.Fatalf("nil Info = %+v", got)
	}
	lock, err := acquire(t, newLockPath(t), "serve")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release err = %v", err)
	}
}
