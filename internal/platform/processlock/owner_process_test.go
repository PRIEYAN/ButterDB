package processlock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const (
	ownerModeEnv = "PROCESSLOCK_TEST_OWNER_MODE"
	ownerPathEnv = "PROCESSLOCK_TEST_OWNER_PATH"
)

func TestLockOwnerProcess(t *testing.T) {
	mode := os.Getenv(ownerModeEnv)
	if mode == "" {
		return
	}
	path := os.Getenv(ownerPathEnv)
	if mode == "legacy" {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(f).Encode(Info{Role: "serve", PID: os.Getpid(), StartedAt: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
	} else {
		lock, err := Acquire(path, "serve", clock.System{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := lock.Release(); err != nil {
				t.Error(err)
			}
		})
	}
	fmt.Println("owner ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type ownerProcess struct {
	cmd    *exec.Cmd
	pid    int
	waited bool
}

func startOwnerProcess(t *testing.T, path, mode string) *ownerProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockOwnerProcess$")
	cmd.Env = append(os.Environ(), ownerModeEnv+"="+mode, ownerPathEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	owner := &ownerProcess{cmd: cmd, pid: cmd.Process.Pid}
	t.Cleanup(func() {
		if !owner.waited {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "owner ready\n" {
		t.Fatalf("owner readiness=%q error=%v", line, err)
	}
	return owner
}

func (p *ownerProcess) crash(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	p.waited = true
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Fatalf("crashed owner exit=%v", err)
	}
}

func TestAcquireAfterOwnerProcessDiesWithoutRelease(t *testing.T) {
	path := newLockPath(t)
	owner := startOwnerProcess(t, path, "kernel")
	lock, err := acquire(t, path, "rebuild")
	if !errors.Is(err, ErrAlreadyRunning) {
		if lock != nil {
			_ = lock.Release()
		}
		t.Fatalf("live owner allowed competing backend: %v", err)
	}
	info, active, err := ReadActive(path)
	if err != nil || !active || info.PID != owner.pid {
		t.Fatalf("live owner info=%+v active=%v error=%v, want pid %d", info, active, err, owner.pid)
	}
	owner.crash(t)
	_, active, err = ReadActive(path)
	if err != nil || active {
		t.Fatalf("dead owner active=%v error=%v", active, err)
	}
	recovered, err := acquire(t, path, "rebuild")
	if err != nil {
		t.Fatalf("crashed owner blocked new backend: %v", err)
	}
	t.Cleanup(func() {
		if err := recovered.Release(); err != nil {
			t.Error(err)
		}
	})
}

func TestUpgradeDoesNotTakeOverLiveLegacyOwner(t *testing.T) {
	path := newLockPath(t)
	owner := startOwnerProcess(t, path, "legacy")
	if lock, err := acquire(t, path, "rebuild"); !errors.Is(err, ErrAlreadyRunning) {
		if lock != nil {
			_ = lock.Release()
		}
		t.Fatalf("live older backend lost exclusion: %v", err)
	}
	owner.crash(t)
	lock, err := acquire(t, path, "serve")
	if err != nil {
		t.Fatalf("dead legacy owner blocked migration: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	})
	info, err := Read(path)
	if err != nil || info.LockVersion != 1 {
		t.Fatalf("migrated metadata=%+v error=%v", info, err)
	}
}

func TestProcessRunning(t *testing.T) {
	path := newLockPath(t)
	owner := startOwnerProcess(t, path, "kernel")
	for _, tc := range []struct {
		name string
		pid  int
		want bool
	}{
		{"this process", os.Getpid(), true},
		{"live child process", owner.pid, true},
		{"zero pid", 0, false},
		{"negative pid", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProcessRunning(tc.pid); got != tc.want {
				t.Fatalf("ProcessRunning(%d) = %v, want %v", tc.pid, got, tc.want)
			}
		})
	}
	owner.crash(t)
	if ProcessRunning(owner.pid) {
		t.Fatalf("ProcessRunning(%d) = true after the process was reaped", owner.pid)
	}
}
