package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

func testDeps(t *testing.T, dataDir string) Dependencies {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = dataDir
	return Dependencies{
		Config:    cfg,
		Role:      "serve",
		LogOutput: &bytes.Buffer{},
		Clock:     clock.NewFake(time.Unix(1763000000, 0)),
		FS:        fsx.OS{},
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNewRejectsNilDependencies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"nil log output", func(d *Dependencies) { d.LogOutput = nil }},
		{"nil clock", func(d *Dependencies) { d.Clock = nil }},
		{"nil filesystem", func(d *Dependencies) { d.FS = nil }},
		{"empty role", func(d *Dependencies) { d.Role = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := testDeps(t, privateDir(t))
			tc.mutate(&deps)
			app, err := New(deps)
			if app != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New = %v, %v; want ErrMissingDependency", app, err)
			}
		})
	}
}

func TestStartTakesLockAndCloseReleasesIt(t *testing.T) {
	dir := privateDir(t)
	first, err := New(testDeps(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if first.DataDir() == nil || first.Logger() == nil {
		t.Fatal("started app is missing data directory or logger")
	}

	second, err := New(testDeps(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	err = second.Start(context.Background())
	if !errors.Is(err, processlock.ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}

	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	third, err := New(testDeps(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Start(context.Background()); err != nil {
		t.Fatalf("Start after release: %v", err)
	}
	if err := third.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusesLooseDataDir(t *testing.T) {
	dir := privateDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	app, err := New(testDeps(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); !errors.Is(err, datadir.ErrLooseMode) {
		t.Fatalf("Start = %v, want ErrLooseMode", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "butterdbd.lock.guard")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lock guard was created in a refused directory: %v", statErr)
	}
}

func TestLoggerWritesJSONToOutput(t *testing.T) {
	var out bytes.Buffer
	deps := testDeps(t, privateDir(t))
	deps.LogOutput = &out
	app, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	app.Logger().Info("hello")
	if !bytes.Contains(out.Bytes(), []byte(`"msg":"hello"`)) {
		t.Fatalf("log output %q lacks the message", out.String())
	}
}
