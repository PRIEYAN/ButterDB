package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/core"
	tgidentity "github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestRunDispatch(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no arguments", nil, exitUsage, "", "usage:"},
		{"help", []string{"help"}, exitOK, "subcommands:", ""},
		{"unknown subcommand", []string{"frobnicate"}, exitUsage, "", `unknown subcommand "frobnicate"`},
		{"rebuild not built", []string{"rebuild"}, exitFailure, "", "not available in this build"},
		{"bad flag", []string{"version", "--nope"}, exitUsage, "", "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(nil)
			code := ta.app.run(context.Background(), tc.args)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, ta.stderr.String())
			}
			if !strings.Contains(ta.stdout.String(), tc.wantStdout) || !strings.Contains(ta.stderr.String(), tc.wantStderr) {
				t.Fatalf("stdout %q stderr %q lack %q / %q", ta.stdout.String(), ta.stderr.String(), tc.wantStdout, tc.wantStderr)
			}
		})
	}
}

func TestVersionOutput(t *testing.T) {
	ta := newTestApp(nil)
	if code := ta.app.run(context.Background(), []string{"version"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	line := ta.stdout.String()
	if !strings.HasPrefix(line, "butterdbd 1.2.3 (abc1234, go") || !strings.Contains(line, "/") {
		t.Fatalf("version line %q", line)
	}

	ta = newTestApp(nil)
	if code := ta.app.run(context.Background(), []string{"version", "--json"}); code != exitOK {
		t.Fatalf("json exit %d", code)
	}
	var report versionReport
	if err := json.Unmarshal(ta.stdout.buf.Bytes(), &report); err != nil {
		t.Fatalf("version --json is not JSON: %v", err)
	}
	if report.Version != "1.2.3" || report.Commit != "abc1234" || report.GoVersion == "" || report.OS == "" || report.Arch == "" {
		t.Fatalf("report %+v", report)
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, exitOK},
		{"usage", fmt.Errorf("x: %w", errUsage), exitUsage},
		{"invalid configuration", fmt.Errorf("config: %w: 1 error(s)", config.ErrInvalid), exitUsage},
		{"loose data directory", fmt.Errorf("core: %w", datadir.ErrLooseMode), exitUsage},
		{"foreign data directory", fmt.Errorf("core: %w", datadir.ErrForeignOwner), exitUsage},
		{"locked", fmt.Errorf("core: start: %w", processlock.ErrAlreadyRunning), exitLocked},
		{"joined lock failure", errors.Join(errors.New("other"), processlock.ErrAlreadyRunning), exitLocked},
		{"telegram flood wait", fmt.Errorf("core: setup: %w", tgclient.ErrFloodWait), exitTelegram},
		{"telegram unavailable", fmt.Errorf("x: %w", tgidentity.ErrTelegramUnavailable), exitTelegram},
		{"key or registry check", fmt.Errorf("core: %w", core.ErrKeyOrRegistry), exitKey},
		{"incompatible data", fmt.Errorf("core: %w", core.ErrIncompatibleData), exitIncompat},
		{"anything else", errors.New("boom"), exitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor = %d, want %d", got, tc.want)
			}
		})
	}
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func serveEnviron(t *testing.T, dir string) []string {
	t.Helper()
	return []string{
		"BUTTERDB_DATA_DIR=" + dir,
		"BUTTERDB_SERVER_KEY_FILE=" + filepath.Join(dir, "server.key"),
		"BUTTERDB_ADMIN_LISTEN=" + freeLoopbackAddress(t),
	}
}

func TestServeSecondInstanceExitsThree(t *testing.T) {
	dir := privateTempDir(t)
	environ := serveEnviron(t, dir)

	first := newTestApp(environ)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := runInBackground(first.app, ctx, []string{"serve"})
	waitFor(t, "first serve to start", func() bool { return strings.Contains(first.stdout.String(), "serve started") })

	second := newTestApp(environ)
	if code := second.app.run(context.Background(), []string{"serve"}); code != exitLocked {
		t.Fatalf("second serve exit %d, want %d (stderr %q)", code, exitLocked, second.stderr.String())
	}
	if !strings.Contains(second.stderr.String(), "already running") {
		t.Fatalf("second serve stderr %q does not name the lock", second.stderr.String())
	}

	stop()
	if code := <-done; code != exitOK {
		t.Fatalf("first serve exit %d after shutdown", code)
	}

	third := newTestApp(environ)
	ctx3, stop3 := context.WithCancel(context.Background())
	done3 := runInBackground(third.app, ctx3, []string{"serve"})
	waitFor(t, "serve after release", func() bool { return strings.Contains(third.stdout.String(), "serve started") })
	stop3()
	if code := <-done3; code != exitOK {
		t.Fatalf("third serve exit %d", code)
	}
}

func TestServeKeepsAdminListenerWhenNotLinked(t *testing.T) {
	dir := privateTempDir(t)
	ta := newTestApp(serveEnviron(t, dir))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := runInBackground(ta.app, ctx, []string{"serve"})
	waitFor(t, "serve to start", func() bool { return strings.Contains(ta.stdout.String(), "serve started") })
	stop()
	if code := <-done; code != exitOK {
		t.Fatalf("serve exit %d, stderr %q", code, ta.stderr.String())
	}
	if !strings.Contains(ta.stdout.String(), "s3 listener not started: deployment is not linked") {
		t.Fatalf("stdout %q does not say the s3 listener was skipped", ta.stdout.String())
	}
	for _, want := range []string{`"msg":"admin listener"`, `"tls_fingerprint":"SHA256:`, `"msg":"setup token"`, `"msg":"server key generated"`} {
		if !strings.Contains(ta.stdout.String(), want) {
			t.Fatalf("stdout %q lacks %s", ta.stdout.String(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "control.db")); err != nil {
		t.Fatalf("unlinked serve did not create control.db: %v", err)
	}
}

func TestServeRefusesLooseDataDir(t *testing.T) {
	dir := privateTempDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp([]string{"BUTTERDB_DATA_DIR=" + dir})
	if code := ta.app.run(context.Background(), []string{"serve"}); code != exitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitUsage, ta.stderr.String())
	}
}

func TestServeRejectsInvalidConfiguration(t *testing.T) {
	ta := newTestApp([]string{"BUTTERDB_NOT_A_KEY=1"})
	if code := ta.app.run(context.Background(), []string{"serve"}); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(ta.stderr.String(), "BUTTERDB_NOT_A_KEY") {
		t.Fatalf("stderr %q does not name the variable", ta.stderr.String())
	}
}

func TestServeRefusesInvalidStateFile(t *testing.T) {
	dir := privateTempDir(t)
	keyFile := writeKeyFile(t, dir, 0o600)
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp([]string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyFile})
	if code := ta.app.run(context.Background(), []string{"serve"}); code != exitKey {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitKey, ta.stderr.String())
	}
	if !strings.Contains(ta.stderr.String(), "state_invalid") {
		t.Fatalf("stderr %q does not name the reason", ta.stderr.String())
	}
	if strings.Contains(ta.stdout.String(), "serve started") {
		t.Fatalf("serve started despite a failed startup check: %q", ta.stdout.String())
	}
}
