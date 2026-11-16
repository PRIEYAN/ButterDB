package phase01

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestProcessLockPreventsASecondServer(t *testing.T) {
	binary := buildDaemon(t)
	dir := privateDir(t)
	env := serveEnv(t, dir)

	first := startServe(t, binary, env)
	first.waitForLine(t, "serve started")

	for _, name := range []string{"butterdbd.lock", "butterdbd.lock.guard"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("lock file %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %04o, want 0600", name, info.Mode().Perm())
		}
	}

	second := runDaemon(t, binary, env, "serve")
	if second.code != 3 {
		t.Fatalf("second serve exit %d, want 3 (stderr %q)", second.code, second.stderr)
	}
	if !strings.Contains(second.stderr, "already running") {
		t.Fatalf("second serve stderr %q does not report the running instance", second.stderr)
	}

	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := first.exitCode(t); code != 0 {
		t.Fatalf("first serve exit %d after SIGTERM, want 0", code)
	}

	third := startServe(t, binary, env)
	third.waitForLine(t, "serve started")
	if err := third.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := third.exitCode(t); code != 0 {
		t.Fatalf("serve after release exit %d, want 0", code)
	}
	for _, name := range []string{"butterdbd.lock", "butterdbd.lock.guard"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("lock file %s was removed on release: %v", name, err)
		}
	}
}
