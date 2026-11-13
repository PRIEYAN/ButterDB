package phase01

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type result struct {
	code   int
	stdout string
	stderr string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func buildDaemon(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "butterdbd")
	cmd := exec.Command("go", "build", "-mod=readonly", "-o", binary, "./cmd/butterdbd")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/butterdbd: %v\n%s", err, out)
	}
	return binary
}

func cleanEnv(extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
	return append(env, extra...)
}

func runDaemon(t *testing.T, binary string, env []string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

type server struct {
	cmd   *exec.Cmd
	lines chan string
	done  chan struct{}
	err   error
}

func startServe(t *testing.T, binary string, env []string) *server {
	t.Helper()
	cmd := exec.Command(binary, "serve")
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{cmd: cmd, lines: make(chan string, 64), done: make(chan struct{})}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case s.lines <- scanner.Text():
			default:
			}
		}
		s.err = cmd.Wait()
		close(s.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-s.done
	})
	return s
}

func (s *server) waitForLine(t *testing.T, fragment string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case line := <-s.lines:
			if strings.Contains(line, fragment) {
				return
			}
		case <-s.done:
			t.Fatalf("server exited before logging %q: %v", fragment, s.err)
		case <-deadline:
			t.Fatalf("timed out waiting for log line %q", fragment)
		}
	}
}

func (s *server) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("server did not exit")
	}
	var exitErr *exec.ExitError
	if errors.As(s.err, &exitErr) {
		return exitErr.ExitCode()
	}
	if s.err != nil {
		t.Fatalf("wait: %v", s.err)
	}
	return 0
}
