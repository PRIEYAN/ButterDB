package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/viewer"
)

func fakeMPV(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	file := filepath.Join(t.TempDir(), "mpv")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestMPVEmbedsAndDisablesDiskCache(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	quoted := "'" + strings.ReplaceAll(argsFile, "'", "'\\''") + "'"
	binary := fakeMPV(t, "printf '%s\\n' \"$@\" > "+quoted+"\n")
	player := mpvPlayer{executable: binary}
	source := "http://127.0.0.1:1234/private-token"
	if err := player.Play(context.Background(), 42, source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, want := range []string{"--wid=42", "--osc=yes", "--cache-on-disk=no", "--no-config", "--load-scripts=no", source} {
		found := false
		for _, arg := range args {
			found = found || arg == want
		}
		if !found {
			t.Fatalf("MPV arguments lack %q: %q", want, args)
		}
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != source {
		t.Fatalf("source must follow option terminator: %q", args)
	}
}

func TestMPVStopsWhenContextIsCancelled(t *testing.T) {
	player := mpvPlayer{executable: fakeMPV(t, "exec sleep 30\n")}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := player.Play(ctx, 42, "http://127.0.0.1:1234/private-token")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation = %v after %s", err, time.Since(start))
	}
}

func TestMPVReportsMissingPlayerAndFailedPlayback(t *testing.T) {
	missing := mpvPlayer{executable: filepath.Join(t.TempDir(), "missing-mpv")}
	if err := missing.Play(context.Background(), 42, "http://127.0.0.1/file"); !errors.Is(err, viewer.ErrPlayerUnavailable) {
		t.Fatalf("missing player error = %v", err)
	}
	player := mpvPlayer{executable: fakeMPV(t, "exit 1\n")}
	err := player.Play(context.Background(), 42, "http://127.0.0.1/private-token")
	if err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("playback failure = %v", err)
	}
}
