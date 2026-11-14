package core

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const testAPIHash = "0123456789abcdef0123456789abcdef"

func sessionDeps(t *testing.T, dataDir string) Dependencies {
	t.Helper()
	deps := testDeps(t, dataDir)
	secrets := filepath.Join(dataDir, "secrets")
	deps.Config.ServerKeyFile = filepath.Join(secrets, "server.key")
	deps.Config.Telegram.AppFile = filepath.Join(secrets, "telegram_app.json")
	deps.Config.Telegram.SessionFile = filepath.Join(secrets, "session_primary.json")
	deps.Config.Telegram.ReplicaSessionFile = filepath.Join(secrets, "session_replica.json")
	return deps
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openSessionFor(t *testing.T, app *App, role identity.Role) *TelegramSession {
	t.Helper()
	if _, err := os.Stat(app.cfg.ServerKeyFile); errors.Is(err, fs.ErrNotExist) {
		if _, err := keyring.Generate(app.cfg.ServerKeyFile, cryptorand.Reader); err != nil {
			t.Fatalf("generate key: %v", err)
		}
	}
	session, err := app.OpenTelegram(context.Background(), role)
	if err != nil {
		t.Fatalf("OpenTelegram: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

func startApp(t *testing.T, deps Dependencies) *App {
	t.Helper()
	app, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return app
}

func TestNewFlowControlRejectsMissingDependencies(t *testing.T) {
	complete := FlowDependencies{
		SendRate: config.Default().Telegram.SendRate,
		Clock:    clock.NewFake(time.Unix(1763000000, 0)),
		Sleeper:  flow.SystemSleeper{},
		Random:   cryptorand.Reader,
		Logger:   newDiscardLogger(),
	}
	cases := []struct {
		name   string
		mutate func(*FlowDependencies)
	}{
		{"nil clock", func(d *FlowDependencies) { d.Clock = nil }},
		{"nil sleeper", func(d *FlowDependencies) { d.Sleeper = nil }},
		{"nil random", func(d *FlowDependencies) { d.Random = nil }},
		{"nil logger", func(d *FlowDependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := complete
			tc.mutate(&deps)
			if got, err := NewFlowControl(deps); got != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewFlowControl = %v, %v; want ErrMissingDependency", got, err)
			}
		})
	}
	control, err := NewFlowControl(complete)
	if err != nil {
		t.Fatalf("NewFlowControl: %v", err)
	}
	if control.ControlRetry.MaxRetries == 0 || control.UploadRetry.RetryTransportUntilCanceled == false {
		t.Fatalf("retry policies lost their defaults: %+v %+v", control.ControlRetry, control.UploadRetry)
	}
}

func TestNewTelegramRejectsMissingDependencies(t *testing.T) {
	dataDir := privateDir(t)
	app := startApp(t, sessionDeps(t, dataDir))
	session := openSessionFor(t, app, identity.RolePrimary)
	complete := TelegramDependencies{
		Config:   app.cfg,
		Role:     identity.RolePrimary,
		Clock:    app.clock,
		FS:       fsx.OS{},
		Random:   cryptorand.Reader,
		Sleeper:  flow.SystemSleeper{},
		Logger:   app.logger,
		Keys:     session.ring,
		Accounts: session.accounts,
	}
	cases := []struct {
		name   string
		mutate func(*TelegramDependencies)
	}{
		{"nil clock", func(d *TelegramDependencies) { d.Clock = nil }},
		{"nil filesystem", func(d *TelegramDependencies) { d.FS = nil }},
		{"nil random", func(d *TelegramDependencies) { d.Random = nil }},
		{"nil sleeper", func(d *TelegramDependencies) { d.Sleeper = nil }},
		{"nil logger", func(d *TelegramDependencies) { d.Logger = nil }},
		{"nil keys", func(d *TelegramDependencies) { d.Keys = nil }},
		{"nil accounts", func(d *TelegramDependencies) { d.Accounts = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := complete
			tc.mutate(&deps)
			if got, err := NewTelegram(deps); got != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewTelegram = %v, %v; want ErrMissingDependency", got, err)
			}
		})
	}
}

func TestSessionKeyLeaseIsIndependentCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.key")
	if _, err := keyring.Generate(path, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := keyring.Load(path)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	defer ring.Close()
	keys := sessionKeys{source: ring}
	first, err := keys.LeaseSessionKey()
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	clear(first)
	second, err := keys.LeaseSessionKey()
	if err != nil {
		t.Fatalf("lease again: %v", err)
	}
	if bytes.Equal(second, make([]byte, len(second))) {
		t.Fatal("clearing one lease zeroed the ring's key")
	}
	ring.Close()
	if _, err := keys.LeaseSessionKey(); !errors.Is(err, errSessionKeyUnavailable) {
		t.Fatalf("lease after close = %v, want errSessionKeyUnavailable", err)
	}
}

func TestOpenTelegramRequiresStartedApp(t *testing.T) {
	app, err := New(sessionDeps(t, privateDir(t)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := app.OpenTelegram(context.Background(), identity.RolePrimary); !errors.Is(err, ErrAppNotStarted) {
		t.Fatalf("OpenTelegram = %v, want ErrAppNotStarted", err)
	}
}

func TestCredentialsSurviveReopeningTheSession(t *testing.T) {
	dataDir := privateDir(t)
	deps := sessionDeps(t, dataDir)
	if _, err := keyring.Generate(deps.Config.ServerKeyFile, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ctx := context.Background()

	first := startApp(t, deps)
	session := openSessionFor(t, first, identity.RolePrimary)
	snap, err := session.Identity().Status(ctx)
	if err != nil || snap.State != identity.StateNoCredentials {
		t.Fatalf("fresh status = %+v, %v; want no_credentials", snap, err)
	}
	if err := session.Identity().SaveCredentials(ctx, 12345, testAPIHash); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("session close: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("app close: %v", err)
	}

	second := startApp(t, deps)
	reopened := openSessionFor(t, second, identity.RolePrimary)
	snap, err = reopened.Identity().Status(ctx)
	if err != nil || snap.State != identity.StateReady || snap.Credentials.APIID != 12345 {
		t.Fatalf("reopened status = %+v, %v; want ready with api id 12345", snap, err)
	}
}

func TestTransportNeedsCredentials(t *testing.T) {
	deps := sessionDeps(t, privateDir(t))
	if _, err := keyring.Generate(deps.Config.ServerKeyFile, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	session := openSessionFor(t, startApp(t, deps), identity.RolePrimary)
	if _, err := session.Transport(context.Background()); !errors.Is(err, identity.ErrCredentialsMissing) {
		t.Fatalf("Transport = %v, want ErrCredentialsMissing", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("session close: %v", err)
	}
	if _, err := session.Transport(context.Background()); !errors.Is(err, errTelegramClosed) {
		t.Fatalf("Transport after close = %v, want errTelegramClosed", err)
	}
}

func TestTransportStartsLazilyAndClosesCleanly(t *testing.T) {
	deps := sessionDeps(t, privateDir(t))
	if _, err := keyring.Generate(deps.Config.ServerKeyFile, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	session := openSessionFor(t, startApp(t, deps), identity.RolePrimary)
	ctx := context.Background()
	if err := session.Identity().SaveCredentials(ctx, 12345, testAPIHash); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	transport, err := session.Transport(ctx)
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	again, err := session.Transport(ctx)
	if err != nil || again != transport {
		t.Fatalf("second Transport = %p, %v; want the first instance %p", again, err, transport)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("session close: %v", err)
	}
}
