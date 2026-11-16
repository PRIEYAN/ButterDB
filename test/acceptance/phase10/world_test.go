package phase10

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	primaryUser   = int64(777000123)
	telegramHash  = "0123456789abcdef0123456789abcdef"
	phoneNumber   = "+447700900134"
	loginCode     = "12345"
	deviceName    = "Office laptop"
	readyDeadline = 30 * time.Second
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) find(msg, attr string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, raw := range bytes.Split(b.buf.Bytes(), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil || line["msg"] != msg {
			continue
		}
		if value, ok := line[attr].(string); ok {
			return value
		}
	}
	return ""
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) Remove(path string) error { return os.Remove(path) }

type sessionDiscard struct{}

func (sessionDiscard) Delete(context.Context) error { return nil }

type accountStore struct {
	repo *accountrepo.Repository
}

func (a accountStore) Get(ctx context.Context, role string) (identity.StoredAccount, bool, error) {
	rec, found, err := a.repo.Get(ctx, role)
	return identity.StoredAccount(rec), found, err
}

func (a accountStore) FindByUser(ctx context.Context, userID int64) (string, bool, error) {
	return a.repo.FindByUser(ctx, userID)
}

func (a accountStore) Link(ctx context.Context, account identity.StoredAccount) error {
	return a.repo.Link(ctx, accountrepo.Record(account))
}

func (a accountStore) Unlink(ctx context.Context, role string, at int64) error {
	return a.repo.Unlink(ctx, role, at)
}

type fakeSource struct {
	transport core.StorageTransport
}

func (f fakeSource) StorageTransport(context.Context) (core.StorageTransport, error) {
	return f.transport, nil
}

type projectionFile struct {
	path string
}

func (p projectionFile) OpenProjectionDatabase(ctx context.Context) (*sqlitedb.DB, error) {
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, p.path, sqlitedb.Options{}, discardLogger())
	if err != nil {
		return nil, err
	}
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type world struct {
	t        *testing.T
	dir      string
	telegram *faketg.Client
	network  *faketg.Network
	starter  *core.StorageStarter
	admin    *core.Admin
	logs     *syncBuffer
	baseURL  string
	spki     string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clk := clock.System{}
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	script := faketg.LoginScript{Phone: phoneNumber, Code: loginCode, CodeLifetime: 5 * time.Minute}
	telegram, err := network.AddAccount(faketg.AccountConfig{UserID: primaryUser, FirstName: "Ada", Username: "ada", Login: &script})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	w := &world{t: t, dir: dir, telegram: telegram, network: network, logs: &syncBuffer{}}
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(ring.Close)
	control, err := sqlitedb.Open(ctx, sqlitedb.Control, filepath.Join(dir, "control.db"), sqlitedb.Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open control: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if _, err := schema.Apply(ctx, control, schema.Control); err != nil {
		t.Fatalf("apply control: %v", err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.ControlSocket = filepath.Join(dir, "admin.sock")
	cfg.S3.Listen = "127.0.0.1:0"
	cfg.S3.PublicURL = "https://s3.example.com"
	cfg.Cache.Dir = filepath.Join(dir, "cache")
	cfg.Telegram.SendRate = config.SendRateConfig{ControlPerMinute: 600, ControlBurst: 100, DocumentsPerMinute: 120, DocumentsBurst: 20}
	accounts, err := accountrepo.New(control)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatal(err)
	}
	login, err := identity.NewService(identity.Dependencies{
		Role: identity.RolePrimary, CredentialsPath: filepath.Join(dir, "telegram_app.json"), Writer: writer,
		Reader: osFiles{}, Remover: osFiles{}, Auth: telegram, Self: telegram, Accounts: accountStore{repo: accounts},
		Sessions: sessionDiscard{}, Clock: clk, Random: cryptorand.Reader, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	flowControl, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: cfg.Telegram.SendRate, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := datadir.Open(fsx.OS{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := core.NewKeyService(control, ring, clk, cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	slot := &core.BackendSlot{}
	logger := slog.New(slog.NewJSONHandler(w.logs, nil))
	w.starter, err = core.NewStorageStarter(core.StorageStarterDependencies{
		Config: cfg, Data: data, FS: fsx.OS{}, Files: fsx.OS{}, Control: control, Ring: ring, Keys: keys, Accounts: accounts,
		Source: fakeSource{transport: core.StorageTransport{
			Channels: telegram, Sender: telegram, Prepaced: telegram, Reader: telegram, Signals: conn.NewActivity(0), Pacer: flowControl.Limiter,
		}},
		Projections: projectionFile{path: filepath.Join(dir, "projection.db")}, Slot: slot, Clock: clk, Sleeper: flow.SystemSleeper{},
		Random: cryptorand.Reader, Logger: logger,
	})
	if err != nil {
		t.Fatalf("starter: %v", err)
	}
	w.starter.Start(ctx)
	t.Cleanup(func() { _ = w.starter.Close(context.Background()) })
	serverID, err := core.EnsureServerID(ctx, control, cryptorand.Reader)
	if err != nil {
		t.Fatalf("server id: %v", err)
	}
	w.admin, err = core.NewAdmin(core.AdminDependencies{
		Config: cfg, Data: data, Control: control, Keys: keys, Relay: login, Messages: telegram, Backend: slot,
		LinkEvents: w.starter, Registry: w.starter, ServerID: serverID, Build: core.BuildInfo{Version: "0.10.0", Commit: "abc1234"},
		Files: fsx.OS{}, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new admin: %v", err)
	}
	if err := w.admin.Start(ctx); err != nil {
		t.Fatalf("start admin: %v", err)
	}
	t.Cleanup(func() { _ = w.admin.Close(context.Background()) })
	w.baseURL = "https://" + w.admin.Addr()
	pin, known := w.admin.Fingerprint()
	if !known {
		t.Fatal("admin has no tls fingerprint")
	}
	w.spki = pin
	return w
}

func (w *world) client(token string) *adminv1.Client {
	w.t.Helper()
	client, err := adminv1.NewClient(adminv1.ClientConfig{
		BaseURL: w.baseURL, Trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: w.spki}, Token: token,
	})
	if err != nil {
		w.t.Fatalf("client: %v", err)
	}
	return client
}

func (w *world) claim() *adminv1.Client {
	w.t.Helper()
	token := w.logs.find("setup token", "setup_token")
	if token == "" {
		w.t.Fatalf("no setup token in the log: %s", w.logs.String())
	}
	creds, err := w.client("").Claim(context.Background(), adminv1.ClaimRequest{
		SetupToken: token, DeviceName: deviceName, Platform: "linux/amd64", ClientVersion: "0.10.0",
	})
	if err != nil {
		w.t.Fatalf("claim: %v", err)
	}
	return w.client(creds.DeviceToken)
}

func (w *world) linkByPhoneAndCode(owner *adminv1.Client) {
	w.t.Helper()
	ctx := context.Background()
	status, err := owner.StartTelegramLogin(ctx, adminv1.TelegramLoginStartRequest{Phone: phoneNumber})
	if err != nil {
		w.t.Fatalf("start login: %v", err)
	}
	if status, err = owner.SubmitTelegramCode(ctx, adminv1.TelegramCodeRequest{LoginID: status.Login.LoginID, Code: loginCode}); err != nil {
		w.t.Fatalf("submit code: %v", err)
	}
	if status.State != adminv1.TelegramLinked {
		w.t.Fatalf("state after code = %s, want linked", status.State)
	}
}

func (w *world) awaitReady(owner *adminv1.Client) adminv1.Status {
	w.t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(readyDeadline)
	var last adminv1.Status
	for time.Now().Before(deadline) {
		status, err := owner.Status(ctx)
		if err != nil {
			w.t.Fatalf("status: %v", err)
		}
		if status.State == "ready" {
			return status
		}
		last = status
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("server never became ready; last status %+v; logs %s", last, w.logs.String())
	return last
}
