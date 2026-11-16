package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

var (
	setupTokenLine  = regexp.MustCompile(`^setup token: bdst1_[a-z2-7]{32} \(expires \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\)\n$`)
	pairingCodeLine = regexp.MustCompile(`^pairing code: [0-9A-Z]{5}-[0-9A-Z]{5} \(expires \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\)\n$`)
)

func TestAdminTokenWithoutARunningServer(t *testing.T) {
	dir := privateTempDir(t)
	environ := serveEnviron(t, dir)

	first := newTestApp(environ)
	if code := first.app.run(context.Background(), []string{"admin-token"}); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, first.stderr.String())
	}
	if !setupTokenLine.MatchString(first.stdout.String()) {
		t.Fatalf("stdout %q is not the setup token line", first.stdout.String())
	}
	second := newTestApp(environ)
	if code := second.app.run(context.Background(), []string{"admin-token"}); code != exitOK || second.stdout.String() == first.stdout.String() {
		t.Fatalf("second exit %d stdout %q, want a replacement token", code, second.stdout.String())
	}
}

func TestAdminTokenAnswersOverTheControlSocket(t *testing.T) {
	dir := privateTempDir(t)
	environ := serveEnviron(t, dir)
	server := newTestApp(environ)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := runInBackground(server.app, ctx, []string{"serve"})
	waitFor(t, "serve to start", func() bool { return strings.Contains(server.stdout.String(), "serve started") })

	cli := newTestApp(environ)
	if code := cli.app.run(context.Background(), []string{"admin-token"}); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, cli.stderr.String())
	}
	if !setupTokenLine.MatchString(cli.stdout.String()) {
		t.Fatalf("stdout %q is not the setup token line", cli.stdout.String())
	}
	stop()
	if code := <-done; code != exitOK {
		t.Fatalf("serve exit %d", code)
	}
}

func TestAdminTokenRefusesWhenTheServerHoldsTheLockWithoutASocket(t *testing.T) {
	dir := privateTempDir(t)
	held, err := processlock.Acquire(dir+"/butterdbd.lock", "serve", clock.System{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release()
	ta := newTestApp(serveEnviron(t, dir))
	if code := ta.app.run(context.Background(), []string{"admin-token"}); code != exitFailure {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitFailure, ta.stderr.String())
	}
	if !strings.Contains(ta.stderr.String(), "server is running but the control socket is unreachable") || ta.stdout.String() != "" {
		t.Fatalf("stdout %q stderr %q", ta.stdout.String(), ta.stderr.String())
	}
}

func TestAdminTokenRejectsBadFlags(t *testing.T) {
	ta := newTestApp(nil)
	if code := ta.app.run(context.Background(), []string{"admin-token", "--nope"}); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
}

func TestPrintAdminTokenLabelsPairingCodes(t *testing.T) {
	ta := newTestApp(nil)
	ta.app.printAdminToken("pairing_code", "ABCDE-FGHJK", clock.NewFake(testInstant).Now())
	if !pairingCodeLine.MatchString(ta.stdout.String()) {
		t.Fatalf("stdout %q is not the pairing code line", ta.stdout.String())
	}
}

var testInstant = time.Date(2026, 11, 12, 9, 51, 0, 0, time.UTC)
