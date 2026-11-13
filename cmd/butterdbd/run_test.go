package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
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
		{"setup not built", []string{"setup"}, exitFailure, "", "not available in this build"},
		{"tgprobe not built", []string{"tgprobe"}, exitFailure, "", "not available in this build"},
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

func TestServeSecondInstanceExitsThree(t *testing.T) {
	dir := privateTempDir(t)
	environ := []string{"BUTTERDB_DATA_DIR=" + dir}

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
