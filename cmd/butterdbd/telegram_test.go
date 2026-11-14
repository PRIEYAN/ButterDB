package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const testAPIHash = "0123456789abcdef0123456789abcdef"

func telegramEnviron(t *testing.T) ([]string, string) {
	t.Helper()
	dir := privateTempDir(t)
	keyPath := filepath.Join(t.TempDir(), "server.key")
	if _, err := keyring.Generate(keyPath, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return []string{"BUTTERDB_DATA_DIR=" + dir, "BUTTERDB_SERVER_KEY_FILE=" + keyPath}, dir
}

func (ta testApp) withStdin(input string) testApp {
	ta.app.stdin = strings.NewReader(input)
	return ta
}

func TestConsoleTerminalReadsLines(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"plain line", "12345\n", "12345", nil},
		{"carriage return line feed", "12345\r\n", "12345", nil},
		{"last line without newline", "12345", "12345", nil},
		{"empty input", "", "", io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			terminal := newConsoleTerminal(strings.NewReader(tc.input), &out)
			got, err := terminal.Prompt("api_id")
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Prompt = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
			if !strings.HasPrefix(out.String(), "api_id: ") {
				t.Fatalf("prompt output %q lacks the label", out.String())
			}
		})
	}
}

func TestConsoleTerminalSecretIsNotEchoed(t *testing.T) {
	var out bytes.Buffer
	terminal := newConsoleTerminal(strings.NewReader("s3cret\n"), &out)
	got, err := terminal.PromptSecret("two-step password")
	if err != nil || got != "s3cret" {
		t.Fatalf("PromptSecret = %q, %v", got, err)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Fatalf("terminal output %q echoed the secret", out.String())
	}
}

func TestSetupRejectsUnknownRole(t *testing.T) {
	ta := newTestApp(nil).withStdin("")
	if code := ta.app.run(context.Background(), []string{"setup", "--role", "tertiary"}); code != exitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitUsage, ta.stderr.String())
	}
	if !strings.Contains(ta.stderr.String(), "--role") {
		t.Fatalf("stderr %q does not name the flag", ta.stderr.String())
	}
}

func TestSetupSavesCredentialsThenStopsWhenInputEnds(t *testing.T) {
	environ, dir := telegramEnviron(t)
	ta := newTestApp(environ).withStdin("12345\n" + testAPIHash + "\n")
	if code := ta.app.run(context.Background(), []string{"setup"}); code != exitFailure {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitFailure, ta.stderr.String())
	}
	appFile := filepath.Join(dir, "secrets", "telegram_app.json")
	info, err := os.Stat(appFile)
	if err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode %v, want 0600", info.Mode().Perm())
	}
	if strings.Contains(ta.stdout.String(), testAPIHash) || strings.Contains(ta.stderr.String(), testAPIHash) {
		t.Fatal("setup printed the api_hash")
	}
	if !strings.Contains(ta.stdout.String(), "phone number with country code") {
		t.Fatalf("stdout %q never reached the phone prompt", ta.stdout.String())
	}
}

func TestSetupRefusesWhileAnotherInstanceRuns(t *testing.T) {
	environ, _ := telegramEnviron(t)
	server := newTestApp(environ)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := runInBackground(server.app, ctx, []string{"serve"})
	waitFor(t, "serve to start", func() bool { return strings.Contains(server.stdout.String(), "serve started") })

	ta := newTestApp(environ).withStdin("")
	if code := ta.app.run(context.Background(), []string{"setup"}); code != exitLocked {
		t.Fatalf("setup exit %d, want %d (stderr %q)", code, exitLocked, ta.stderr.String())
	}
	stop()
	<-done
}

func TestTGProbeRejectsUnknownStep(t *testing.T) {
	ta := newTestApp(nil).withStdin("")
	if code := ta.app.run(context.Background(), []string{"tgprobe", "--steps", "control,ping"}); code != exitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitUsage, ta.stderr.String())
	}
}

func TestTGProbeNeedsALinkedAccount(t *testing.T) {
	environ, _ := telegramEnviron(t)
	ta := newTestApp(environ).withStdin("")
	if code := ta.app.run(context.Background(), []string{"tgprobe", "--yes"}); code != exitTelegram {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitTelegram, ta.stderr.String())
	}
	if !strings.Contains(ta.stderr.String(), "setup") {
		t.Fatalf("stderr %q does not point at setup", ta.stderr.String())
	}
}

func TestProbeDeclinedErrorIsNotATelegramFailure(t *testing.T) {
	if telegramFailure(errProbeDeclined) {
		t.Fatal("declining the probe must not be reported as a telegram failure")
	}
	if !telegramFailure(tgclient.ErrNotAuthorized) {
		t.Fatal("an unauthorized account is a telegram failure")
	}
}

func TestProbeReportFileIsWrittenPrivately(t *testing.T) {
	ta := newTestApp(nil)
	out := filepath.Join(t.TempDir(), "report.json")
	if err := ta.app.writeProbeReport(out, coreReportForTest()); err != nil {
		t.Fatalf("writeProbeReport: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("report file = %v, %v; want mode 0600", info, err)
	}
	if !strings.Contains(ta.stdout.String(), out) {
		t.Fatalf("stdout %q does not name the report", ta.stdout.String())
	}
}

func coreReportForTest() core.ProbeReport {
	return core.ProbeReport{ProbeVersion: 1, BinaryVersion: "test", AccountUserID: 7}
}
