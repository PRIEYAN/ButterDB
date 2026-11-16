package phase09

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/access/keys"
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
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	primaryUser  = int64(777000123)
	telegramHash = "0123456789abcdef0123456789abcdef"
	phoneNumber  = "+447700900134"
	loginCode    = "12345"
	loginPass    = "correct horse battery"
	s3PublicURL  = "https://s3.example.com"
	deviceName   = "Office laptop"
)

type seqReader struct {
	seed    string
	counter uint64
	buf     []byte
}

func newSeqReader(seed string) *seqReader {
	return &seqReader{seed: seed}
}

func (r *seqReader) Read(p []byte) (int, error) {
	for len(r.buf) < len(p) {
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], r.counter)
		r.counter++
		sum := sha256.Sum256(append([]byte(r.seed), counter[:]...))
		r.buf = append(r.buf, sum[:]...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, raw := range bytes.Split(b.buf.Bytes(), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) == nil {
			out = append(out, line)
		}
	}
	return out
}

func (b *syncBuffer) find(msg, attr string) string {
	for _, line := range b.lines() {
		if line["msg"] == msg {
			if value, ok := line[attr].(string); ok {
				return value
			}
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

type world struct {
	t        *testing.T
	dir      string
	clk      *clock.Fake
	network  *faketg.Network
	telegram *faketg.Client
	ring     *keyring.Ring
	control  *sqlitedb.DB
	db       *sqlitedb.DB
	identity *identity.Service
	slot     *core.BackendSlot
	admin    *core.Admin
	logs     *syncBuffer
	random   *seqReader
	sleeper  flow.Sleeper
	serverID string
	baseURL  string
	spki     string
	keys     *keys.Service
	cfg      config.Config
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Now())
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	script := faketg.LoginScript{Phone: phoneNumber, Code: loginCode, Password: loginPass, PasswordHint: "the usual", CodeLifetime: 5 * time.Minute}
	telegram, err := network.AddAccount(faketg.AccountConfig{UserID: primaryUser, FirstName: "Ada", Username: "ada", Login: &script})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	w := &world{t: t, dir: dir, clk: clk, network: network, telegram: telegram, slot: &core.BackendSlot{}, logs: &syncBuffer{},
		random: newSeqReader("phase09 randomness"), sleeper: flow.SleeperFunc(func(ctx context.Context, wait time.Duration) error {
			clk.Advance(wait)
			return ctx.Err()
		})}
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, newSeqReader("server key")); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if w.ring, err = keyring.LoadWithRand(keyPath, newSeqReader("ring randomness")); err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(w.ring.Close)
	w.control = w.open(sqlitedb.Control, schema.Control, "control.db")
	w.db = w.open(sqlitedb.Projection, schema.Projection, "projection.db")
	w.buildIdentity()
	w.cfg = w.adminConfig()
	return w
}

func (w *world) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(w.logs, nil))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (w *world) open(name sqlitedb.Name, set schema.Set, file string) *sqlitedb.DB {
	w.t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, name, filepath.Join(w.dir, file), sqlitedb.Options{}, discardLogger())
	if err != nil {
		w.t.Fatalf("open %s: %v", file, err)
	}
	w.t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, set); err != nil {
		w.t.Fatalf("apply %s: %v", file, err)
	}
	return db
}

func (w *world) buildIdentity() {
	w.t.Helper()
	repo, err := accountrepo.New(w.control)
	if err != nil {
		w.t.Fatalf("account repository: %v", err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		w.t.Fatal(err)
	}
	w.identity, err = identity.NewService(identity.Dependencies{
		Role: identity.RolePrimary, CredentialsPath: filepath.Join(w.dir, "secrets", "telegram_app.json"), Writer: writer,
		Reader: osFiles{}, Remover: osFiles{}, Auth: w.telegram, Self: w.telegram, Accounts: accountStore{repo: repo},
		Sessions: sessionDiscard{}, Clock: w.clk, Random: newSeqReader("login ids"), Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("identity: %v", err)
	}
}

func (w *world) adminConfig() config.Config {
	cfg := config.Default()
	cfg.DataDir = w.dir
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.ControlSocket = filepath.Join(w.dir, "admin.sock")
	cfg.S3.PublicURL = s3PublicURL
	cfg.Cache.Dir = filepath.Join(w.dir, "cache")
	cfg.Telegram.SendRate = config.SendRateConfig{ControlPerMinute: 600, ControlBurst: 100, DocumentsPerMinute: 120, DocumentsBurst: 20}
	return cfg
}

func (w *world) startAdmin() *core.Admin {
	w.t.Helper()
	if w.serverID == "" {
		id, err := core.EnsureServerID(context.Background(), w.control, w.random)
		if err != nil {
			w.t.Fatalf("server id: %v", err)
		}
		w.serverID = id
	}
	data, err := datadir.Open(fsx.OS{}, w.dir)
	if err != nil {
		w.t.Fatalf("data dir: %v", err)
	}
	keys, err := core.NewKeyService(w.control, w.ring, w.clk, w.random)
	if err != nil {
		w.t.Fatalf("key service: %v", err)
	}
	w.keys = keys
	admin, err := core.NewAdmin(core.AdminDependencies{
		Config: w.cfg, Data: data, Control: w.control, Keys: keys, Relay: w.identity, Messages: w.telegram, Backend: w.slot,
		ServerID: w.serverID, Build: core.BuildInfo{Version: "0.9.0", Commit: "abc1234"}, Files: fsx.OS{}, Clock: w.clk,
		Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: w.logger(),
	})
	if err != nil {
		w.t.Fatalf("new admin: %v", err)
	}
	if err := admin.Start(context.Background()); err != nil {
		w.t.Fatalf("start admin: %v", err)
	}
	w.t.Cleanup(func() { _ = admin.Close(context.Background()) })
	w.admin = admin
	w.baseURL = "https://" + admin.Addr()
	pin, known := admin.Fingerprint()
	if !known {
		w.t.Fatal("admin has no tls fingerprint")
	}
	w.spki = pin
	return admin
}

func (w *world) stopAdmin() {
	w.t.Helper()
	if err := w.admin.Close(context.Background()); err != nil {
		w.t.Fatalf("close admin: %v", err)
	}
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

func (w *world) setupToken() string {
	w.t.Helper()
	token := w.logs.find("setup token", "setup_token")
	if token == "" {
		w.t.Fatalf("no setup token in the log: %s", w.logs.String())
	}
	return token
}

func (w *world) claim() (*adminv1.Client, adminv1.DeviceCredentials) {
	w.t.Helper()
	creds, err := w.client("").Claim(context.Background(), adminv1.ClaimRequest{
		SetupToken: w.setupToken(), DeviceName: deviceName, Platform: "linux/amd64", ClientVersion: "0.9.0",
	})
	if err != nil {
		w.t.Fatalf("claim: %v", err)
	}
	return w.client(creds.DeviceToken), creds
}

func (w *world) linkTelegram(client *adminv1.Client) {
	w.t.Helper()
	ctx := context.Background()
	if _, err := client.SaveTelegramCredentials(ctx, adminv1.TelegramCredentialsRequest{APIID: 1234567, APIHash: telegramHash}); err != nil {
		w.t.Fatalf("save credentials: %v", err)
	}
	status, err := client.StartTelegramLogin(ctx, adminv1.TelegramLoginStartRequest{Phone: phoneNumber})
	if err != nil {
		w.t.Fatalf("start login: %v", err)
	}
	loginID := status.Login.LoginID
	_, err = client.SubmitTelegramCode(ctx, adminv1.TelegramCodeRequest{LoginID: loginID, Code: "00000"})
	requireCode(w.t, err, adminv1.CodeTelegramCodeInvalid)
	if status, err = client.SubmitTelegramCode(ctx, adminv1.TelegramCodeRequest{LoginID: loginID, Code: loginCode}); err != nil {
		w.t.Fatalf("submit code: %v", err)
	}
	if status.State != adminv1.TelegramAwaitingPassword {
		w.t.Fatalf("state after code = %s, want awaiting_password", status.State)
	}
	_, err = client.SubmitTelegramPassword(ctx, adminv1.TelegramPasswordRequest{LoginID: loginID, Password: "wrong"})
	requireCode(w.t, err, adminv1.CodeTelegramPasswordInvalid)
	if status, err = client.SubmitTelegramPassword(ctx, adminv1.TelegramPasswordRequest{LoginID: loginID, Password: loginPass}); err != nil {
		w.t.Fatalf("submit password: %v", err)
	}
	if status.State != adminv1.TelegramLinked {
		w.t.Fatalf("state after password = %s, want linked", status.State)
	}
}

func requireCode(t *testing.T, err error, want adminv1.Code) *adminv1.APIError {
	t.Helper()
	var apiErr *adminv1.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *adminv1.APIError with code %s", err, want)
	}
	if apiErr.Body.Code != want {
		t.Fatalf("error code = %s (status %d), want %s", apiErr.Body.Code, apiErr.Status, want)
	}
	return apiErr
}

func requireStatus(t *testing.T, err error, want int) *adminv1.APIError {
	t.Helper()
	var apiErr *adminv1.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != want {
		t.Fatalf("error = %v, want an *adminv1.APIError with status %d", err, want)
	}
	return apiErr
}
