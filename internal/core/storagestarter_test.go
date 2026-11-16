package core

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	linkUser     = int64(777000123)
	linkPhone    = "+447700900134"
	linkCode     = "12345"
	linkAPIHash  = "0123456789abcdef0123456789abcdef"
	linkDeadline = 20 * time.Second
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []map[string]any {
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

func (b *lockedBuffer) count(level, msgPrefix string) int {
	n := 0
	for _, line := range b.lines() {
		msg, _ := line["msg"].(string)
		if line["level"] == level && strings.HasPrefix(msg, msgPrefix) {
			n++
		}
	}
	return n
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func slogJSON(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

func (b *lockedBuffer) countMessage(msg string) int {
	n := 0
	for _, line := range b.lines() {
		if line["msg"] == msg {
			n++
		}
	}
	return n
}

type flakySource struct {
	inner     StorageTransport
	failures  atomic.Int32
	failFirst int32
}

func (f *flakySource) StorageTransport(context.Context) (StorageTransport, error) {
	if f.failures.Add(1) <= f.failFirst {
		return StorageTransport{}, errors.New("telegram unreachable")
	}
	return f.inner, nil
}

type fileProjections struct {
	ctx  context.Context
	path string
}

func (p fileProjections) OpenProjectionDatabase(ctx context.Context) (*sqlitedb.DB, error) {
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, p.path, sqlitedb.Options{}, newDiscardLogger())
	if err != nil {
		return nil, err
	}
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type discardSessions struct{}

func (discardSessions) Delete(context.Context) error { return nil }

type linkWorld struct {
	t        *testing.T
	dir      string
	cfg      config.Config
	network  *faketg.Network
	telegram *faketg.Client
	ring     *keyring.Ring
	control  *sqlitedb.DB
	identity *identity.Service
	accounts *accountrepo.Repository
	slot     *BackendSlot
	starter  *StorageStarter
	relay    *limitedRelay
	source   *flakySource
	logs     *lockedBuffer
	keys     *keys.Service
	data     *datadir.Dir
	admin    *Admin
}

func newLinkWorld(t *testing.T, failFirst int32) *linkWorld {
	t.Helper()
	ctx := context.Background()
	dir := privateDir(t)
	clk := clock.System{}
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatal(err)
	}
	script := faketg.LoginScript{Phone: linkPhone, Code: linkCode, CodeLifetime: 5 * time.Minute}
	telegram, err := network.AddAccount(faketg.AccountConfig{UserID: linkUser, FirstName: "Ada", Username: "ada", Login: &script})
	if err != nil {
		t.Fatal(err)
	}
	w := &linkWorld{t: t, dir: dir, network: network, telegram: telegram, slot: &BackendSlot{}, logs: &lockedBuffer{}}
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, cryptorand.Reader); err != nil {
		t.Fatal(err)
	}
	if w.ring, err = keyring.Load(keyPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.ring.Close)
	w.control, err = sqlitedb.Open(ctx, sqlitedb.Control, filepath.Join(dir, controlDatabaseFile), sqlitedb.Options{}, newDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.control.Close() })
	if _, err := schema.Apply(ctx, w.control, schema.Control); err != nil {
		t.Fatal(err)
	}
	w.cfg = config.Default()
	w.cfg.DataDir = dir
	w.cfg.S3.Listen = "127.0.0.1:0"
	w.cfg.Admin.Listen = "127.0.0.1:0"
	w.cfg.Admin.ControlSocket = filepath.Join(dir, "admin.sock")
	w.cfg.Cache.Dir = filepath.Join(dir, "cache")
	w.cfg.Telegram.SendRate = config.SendRateConfig{ControlPerMinute: 600, ControlBurst: 100, DocumentsPerMinute: 120, DocumentsBurst: 20}
	w.buildIdentity(clk)
	w.buildStarter(clk, failFirst)
	return w
}

func (w *linkWorld) buildIdentity(clk clock.Clock) {
	w.t.Helper()
	accounts, err := accountrepo.New(w.control)
	if err != nil {
		w.t.Fatal(err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		w.t.Fatal(err)
	}
	w.identity, err = identity.NewService(identity.Dependencies{
		Role: identity.RolePrimary, CredentialsPath: filepath.Join(w.dir, "telegram_app.json"), Writer: writer,
		Reader: osFileReader{}, Remover: fsRemover{fsys: fsx.OS{}}, Auth: w.telegram, Self: w.telegram,
		Accounts: accountStore{records: accounts}, Sessions: discardSessions{}, Clock: clk, Random: cryptorand.Reader,
		Logger: newDiscardLogger(),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.accounts = accounts
}

func (w *linkWorld) buildStarter(clk clock.Clock, failFirst int32) {
	w.t.Helper()
	flowControl, err := NewFlowControl(FlowDependencies{
		SendRate: w.cfg.Telegram.SendRate, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: newDiscardLogger(),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.source = &flakySource{failFirst: failFirst, inner: StorageTransport{
		Channels: w.telegram, Sender: w.telegram, Prepaced: w.telegram, Reader: w.telegram, Signals: conn.NewActivity(0), Pacer: flowControl.Limiter,
	}}
	if w.data, err = datadir.Open(fsx.OS{}, w.dir); err != nil {
		w.t.Fatal(err)
	}
	if w.keys, err = NewKeyService(w.control, w.ring, clk, cryptorand.Reader); err != nil {
		w.t.Fatal(err)
	}
	logger := slogJSON(w.logs)
	w.starter, err = NewStorageStarter(StorageStarterDependencies{
		Config: w.cfg, Data: w.data, FS: fsx.OS{}, Files: fsx.OS{}, Control: w.control, Ring: w.ring, Keys: w.keys,
		Accounts: w.accounts, Source: w.source, Projections: fileProjections{path: filepath.Join(w.dir, projectionDatabaseFile)},
		Slot: w.slot, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: logger,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.starter.Start(context.Background())
	w.t.Cleanup(func() { _ = w.starter.Close(context.Background()) })
	w.relay = &limitedRelay{
		inner: w.identity, limiter: session.NewLimiter(session.DefaultLimiterConfig(), clk), backend: w.slot, links: w.starter,
	}
}

func (w *linkWorld) startAdmin() string {
	w.t.Helper()
	serverID, err := EnsureServerID(context.Background(), w.control, cryptorand.Reader)
	if err != nil {
		w.t.Fatal(err)
	}
	w.admin, err = NewAdmin(AdminDependencies{
		Config: w.cfg, Data: w.data, Control: w.control, Keys: w.keys, Relay: w.identity, Messages: w.telegram, Backend: w.slot,
		LinkEvents: w.starter, Registry: w.starter, ServerID: serverID, Build: BuildInfo{Version: "test"}, Files: fsx.OS{},
		Clock: clock.System{}, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: slogJSON(w.logs),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.admin.Start(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = w.admin.Close(context.Background()) })
	return "https://" + w.admin.Addr()
}

func (w *linkWorld) link() {
	w.t.Helper()
	ctx := context.Background()
	if err := w.relay.SaveCredentials(ctx, 1234567, linkAPIHash); err != nil {
		w.t.Fatalf("save credentials: %v", err)
	}
	snap, err := w.relay.Start(ctx, linkPhone)
	if err != nil || snap.Login == nil {
		w.t.Fatalf("start login: %+v, %v", snap, err)
	}
	if snap, err = w.relay.SubmitCode(ctx, snap.Login.LoginID, linkCode); err != nil || snap.State != identity.StateLinked {
		w.t.Fatalf("submit code: %+v, %v", snap, err)
	}
}

func (w *linkWorld) waitBackend() *Backend {
	w.t.Helper()
	deadline := time.Now().Add(linkDeadline)
	for time.Now().Before(deadline) {
		if backend := w.slot.Current(); backend != nil {
			return backend
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatalf("no backend was published; logs: %v", w.logs.lines())
	return nil
}

func (w *linkWorld) waitReady(backend *Backend) {
	w.t.Helper()
	deadline := time.Now().Add(linkDeadline)
	for time.Now().Before(deadline) {
		if backend.Readiness.State() == router.StateReady {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatal("storage never became ready")
}

func (w *linkWorld) statePath() string {
	return filepath.Join(w.dir, stateFileName)
}

func TestAdminLinkStartsStorageWithoutRestart(t *testing.T) {
	w := newLinkWorld(t, 0)
	if w.slot.Current() != nil || w.starter.currentRuntime() != nil {
		t.Fatal("storage ran before the account was linked")
	}
	if got := w.starter.RegistryReport().Status; got != RegistryAbsent {
		t.Fatalf("registry before link = %q, want absent", got)
	}
	w.link()
	backend := w.waitBackend()
	w.waitReady(backend)
	if _, err := os.Stat(w.statePath()); err != nil {
		t.Fatalf("state.json was not written: %v", err)
	}
	if report := w.starter.RegistryReport(); report.Status != RegistryVerified {
		t.Fatalf("registry report = %+v, want verified", report)
	}
	runtime := w.starter.currentRuntime()
	if runtime == nil || runtime.S3.Addr() == "" {
		t.Fatalf("runtime = %v, want a running runtime with an s3 listener", runtime)
	}
	if got := len(w.network.ChannelIDs()); got != 1 {
		t.Fatalf("channels after link = %d, want only the registry", got)
	}
	ctx := context.Background()
	list, err := backend.Sync.Buckets.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("bucket list = %+v, %v", list, err)
	}
	name, err := bucket.ParseName("photos")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Sync.Buckets.Create(ctx, bucket.Spec{Name: name, Policy: "private", DefaultEncryption: "none"}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if list, err = backend.Sync.Buckets.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("bucket list after create = %+v, %v", list, err)
	}
}

func TestAdminLinkWithExistingRegistryEntersRediscovery(t *testing.T) {
	w := newLinkWorld(t, 0)
	w.link()
	w.waitReady(w.waitBackend())
	if _, err := w.relay.Unlink(context.Background()); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	waitFor(t, func() bool {
		return w.starter.RegistryReport().Status == RegistryAbsent && w.starter.currentRuntime() == nil
	})
	for _, name := range []string{stateFileName, projectionDatabaseFile, projectionDatabaseFile + "-wal", projectionDatabaseFile + "-shm"} {
		if err := os.Remove(filepath.Join(w.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	channelsBefore := len(w.network.ChannelIDs())

	w.link()
	waitFor(t, func() bool { return w.starter.RegistryReport().Status == RegistryRediscovery })
	if w.slot.Current() != nil || w.starter.currentRuntime() != nil {
		t.Fatal("storage started although the account holds an unadopted registry")
	}
	if _, err := os.Stat(w.statePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state.json exists after a refused adoption: %v", err)
	}
	if got := len(w.network.ChannelIDs()); got != channelsBefore {
		t.Fatalf("channels = %d, want %d: a second registry was created", got, channelsBefore)
	}
	if got := w.logs.count("WARN", "registry channels exist"); got != 1 {
		t.Fatalf("rediscovery log lines = %d, want 1", got)
	}
	if !strings.Contains(w.logs.String(), "butterdbd setup --registry-channel") {
		t.Fatalf("log lacks the adoption command: %s", w.logs.String())
	}
	status := w.status(t)
	if status.State != "syncing" || !status.Telegram.Linked {
		t.Fatalf("status = %+v, want syncing and linked", status)
	}
	if !hasComponent(status, "registry", RegistryRediscovery) {
		t.Fatalf("components = %+v, want registry rediscovery", status.Components)
	}
}

func TestAdminLinkStartIsIdempotent(t *testing.T) {
	w := newLinkWorld(t, 0)
	w.link()
	for i := 0; i < 5; i++ {
		go w.starter.AccountLinked()
	}
	backend := w.waitBackend()
	w.waitReady(backend)
	runtime := w.starter.currentRuntime()
	for i := 0; i < 5; i++ {
		w.starter.AccountLinked()
	}
	snap, err := w.identity.Status(context.Background())
	if err != nil || snap.State != identity.StateLinked {
		t.Fatalf("identity status = %+v, %v", snap, err)
	}
	w.relayLinked(snap)
	time.Sleep(200 * time.Millisecond)
	if w.starter.currentRuntime() != runtime || w.slot.Current() != backend {
		t.Fatal("a second link event replaced the running runtime")
	}
	if got := w.logs.countMessage("s3 listener started"); got != 1 {
		t.Fatalf("s3 listeners started = %d, want 1", got)
	}
	if got := len(w.network.ChannelIDs()); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
}

func TestAdminLinkFailureKeepsAdminServing(t *testing.T) {
	w := newLinkWorld(t, 1)
	url := w.startAdmin()
	w.link()
	waitFor(t, func() bool { return w.logs.count("ERROR", "storage start failed") >= 1 })
	if w.slot.Current() != nil {
		t.Fatal("a backend was published after a failed start")
	}
	if _, err := os.Stat(w.statePath()); err == nil {
		t.Fatal("state.json exists after a failed registry setup")
	}
	info := w.serverInfo(t, url)
	if !info.TelegramLinked {
		t.Fatalf("server info = %+v, want a linked deployment", info)
	}
	backend := w.waitBackend()
	w.waitReady(backend)
	if report := w.starter.RegistryReport(); report.Status != RegistryVerified {
		t.Fatalf("registry after the retry = %+v, want verified", report)
	}
}

func TestAdminUnlinkStopsStorageAndRelinkRestoresIt(t *testing.T) {
	w := newLinkWorld(t, 0)
	w.link()
	first := w.waitBackend()
	w.waitReady(first)
	address := w.starter.currentRuntime().S3.Addr()
	if address == "" {
		t.Fatal("no s3 address")
	}
	if _, err := w.relay.Unlink(context.Background()); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	waitFor(t, func() bool {
		return w.starter.RegistryReport().Status == RegistryAbsent && w.starter.currentRuntime() == nil
	})
	if w.slot.Current() != nil {
		t.Fatal("backend still published after unlink")
	}
	w.link()
	second := w.waitBackend()
	w.waitReady(second)
	if second == first {
		t.Fatal("relink reused the stopped backend")
	}
	if got := len(w.network.ChannelIDs()); got != 1 {
		t.Fatalf("channels after relink = %d, want the original registry only", got)
	}
}

func TestStorageStarterRejectsMissingDependencies(t *testing.T) {
	w := newLinkWorld(t, 0)
	if _, err := NewStorageStarter(StorageStarterDependencies{Config: w.cfg}); err == nil {
		t.Fatal("NewStorageStarter accepted empty dependencies")
	}
}

func TestStorageRetryDelayIsBounded(t *testing.T) {
	delay := storageRetryFirst
	for i := 0; i < 20; i++ {
		delay = min(delay*2, storageRetryCeiling)
	}
	if delay != storageRetryCeiling {
		t.Fatalf("delay = %v, want %v", delay, storageRetryCeiling)
	}
}

func (w *linkWorld) relayLinked(snap identity.Snapshot) {
	w.relay.linked(snap)
}

func (w *linkWorld) httpClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func (w *linkWorld) serverInfo(t *testing.T, url string) adminv1.ServerInfo {
	t.Helper()
	resp, err := w.httpClient().Get(url + adminv1.PathServerInfo)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	defer resp.Body.Close()
	var info adminv1.ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	return info
}

func (w *linkWorld) status(t *testing.T) adminv1.Status {
	t.Helper()
	status, err := newAdminStatus(adminStatusDependencies{
		config: w.cfg, relay: w.identity, slot: w.slot, registry: w.starter, clock: clock.System{}, serverID: "test", startedAt: time.Now(),
	}).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return status
}

func hasComponent(status adminv1.Status, name, want string) bool {
	for _, component := range status.Components {
		if component.Name == name {
			return component.Status == want
		}
	}
	return false
}
