package phase07

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/readpath/rangereader"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	primaryUser = int64(777000123)
	baseTime    = int64(1763000000)
	accessKeyID = "BDBK7Q2M9X4T1W8Z3R6Y"
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

type answerTerminal struct{}

func (answerTerminal) Prompt(string) (string, error)       { return "yes", nil }
func (answerTerminal) PromptSecret(string) (string, error) { return "", io.EOF }
func (answerTerminal) Say(string)                          {}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type world struct {
	t          *testing.T
	dir        string
	clk        *clock.Fake
	network    *faketg.Network
	client     *faketg.Client
	ring       *keyring.Ring
	control    *sqlitedb.DB
	db         *sqlitedb.DB
	projection *core.Projection
	activity   *conn.Activity
	writer     *secretfile.Writer
	random     io.Reader
	sleeper    flow.Sleeper
	sync       *core.Sync
	write      *core.Write
	origin     *countingOrigin
	deployment string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(baseTime, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: primaryUser, FirstName: "Ada", Username: "ada"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, newSeqReader("server key")); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := keyring.LoadWithRand(keyPath, newSeqReader("ring randomness"))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(ring.Close)
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, dir: dir, clk: clk, network: network, client: client, ring: ring, writer: writer,
		activity: conn.NewActivity(0), random: newSeqReader("job randomness")}
	w.sleeper = flow.SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		clk.Advance(wait)
		return ctx.Err()
	})
	w.control = w.open(sqlitedb.Control, schema.Control, "control.db")
	w.db = w.open(sqlitedb.Projection, schema.Projection, "projection.db")
	projection, err := core.NewProjection(core.ProjectionDependencies{Logger: discardLogger()})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	w.projection = projection
	w.provisionRegistry()
	w.buildWrite()
	w.origin = &countingOrigin{RangeReader: w.client}
	return w
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

func (w *world) statePath() string {
	return filepath.Join(w.dir, "state.json")
}

func (w *world) provisionRegistry() {
	w.t.Helper()
	outcome, err := core.SetupRegistry(context.Background(), core.RegistrySetupDependencies{
		Database: w.db, Control: w.control, Projection: w.projection, Telegram: w.client, Sender: w.client,
		Signals: w.activity, Ring: w.ring, PrimaryUserID: primaryUser, StatePath: w.statePath(), Writer: w.writer,
		Terminal: answerTerminal{}, Clock: w.clk, Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: discardLogger(),
	}, core.RegistryOptions{})
	if err != nil || !outcome.Created {
		w.t.Fatalf("setup registry = %+v, %v", outcome, err)
	}
	w.deployment = outcome.DeploymentID
	state, err := core.NewRegistryStateStore(w.statePath(), w.writer, w.control, w.random)
	if err != nil {
		w.t.Fatalf("state store: %v", err)
	}
	built, err := core.NewSync(core.SyncDependencies{
		Database: w.db, Control: w.control, Projection: w.projection, Telegram: w.client, Sender: w.client,
		Signals: w.activity, Ring: w.ring, State: state, ActorUserID: primaryUser, DeploymentID: w.deployment,
		Clock: w.clk, Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("new sync: %v", err)
	}
	w.sync = built
}

func (w *world) buildWrite() {
	w.t.Helper()
	cfg := config.Default()
	cfg.Telegram.SendRate = config.SendRateConfig{ControlPerMinute: 600, ControlBurst: 100, DocumentsPerMinute: 120, DocumentsBurst: 20}
	flowControl, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: cfg.Telegram.SendRate, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("flow control: %v", err)
	}
	data, err := datadir.Open(fsx.OS{}, w.dir)
	if err != nil {
		w.t.Fatalf("data dir: %v", err)
	}
	built, err := core.NewWrite(core.WriteDependencies{
		Config: cfg, Data: data, Control: w.control, Database: w.db, Projection: w.projection, Sync: w.sync,
		Telegram: w.client, Sender: w.client, Prepaced: w.client, Pacer: flowControl.Limiter, Ring: w.ring, ActorUserID: primaryUser,
		FS: fsx.OS{}, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("new write: %v", err)
	}
	w.write = built
}

func (w *world) createBucket(name, encryption string) bucket.Info {
	w.t.Helper()
	info, err := w.sync.Buckets.Create(context.Background(), bucket.Spec{Name: bucket.BucketName(name), Policy: "private", DefaultEncryption: encryption, CacheMaxAge: 3600})
	if err != nil {
		w.t.Fatalf("create bucket %s: %v", name, err)
	}
	return info
}

func (w *world) put(bucketName, key string, body []byte, mutate ...func(*object.PutRequest)) (object.ObjectMeta, error) {
	req := object.PutRequest{Bucket: bucketName, Key: object.Key(key), ContentLength: int64(len(body)), ContentType: "text/plain", AccessKeyID: accessKeyID}
	for _, change := range mutate {
		change(&req)
	}
	return w.write.Put.Put(context.Background(), req, bytes.NewReader(body))
}

func (w *world) mustPut(bucketName, key string, body []byte, mutate ...func(*object.PutRequest)) object.ObjectMeta {
	w.t.Helper()
	meta, err := w.put(bucketName, key, body, mutate...)
	if err != nil {
		w.t.Fatalf("put %s/%s: %v", bucketName, key, err)
	}
	return meta
}

type countingOrigin struct {
	tgclient.RangeReader
	mu       sync.Mutex
	resolves int
	reads    int
}

func (c *countingOrigin) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	c.mu.Lock()
	c.resolves++
	c.mu.Unlock()
	return c.RangeReader.ResolveDocument(ctx, peer, msgID)
}

func (c *countingOrigin) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.RangeReader.ReadDocumentRange(ctx, ref, offset, dst)
}

func (c *countingOrigin) calls() (resolves, reads int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolves, c.reads
}

func (w *world) newRead(cacheDir string, ranges *rangereader.RangeReaderConfig) *core.Read {
	w.t.Helper()
	cfg := config.Default()
	cfg.Cache.Dir = cacheDir
	cfg.Cache.BlocksMaxBytes = 64 << 20
	read, err := core.NewRead(core.ReadDependencies{
		Config: cfg, Database: w.db, Sync: w.sync, Telegram: w.client, Origin: w.origin, Ring: w.ring,
		Files: fsx.OS{}, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(), Ranges: ranges,
	})
	if err != nil {
		w.t.Fatalf("new read: %v", err)
	}
	if err := read.Start(context.Background()); err != nil {
		w.t.Fatalf("start read: %v", err)
	}
	return read
}

func (w *world) closeRead(read *core.Read) {
	w.t.Helper()
	if err := read.Close(context.Background()); err != nil {
		w.t.Fatalf("close read: %v", err)
	}
}

func noReadAhead() *rangereader.RangeReaderConfig {
	return &rangereader.RangeReaderConfig{MaxCacheBytes: 64 << 20}
}

func randomBytes(seed string, size int) []byte {
	out := make([]byte, size)
	if _, err := io.ReadFull(newSeqReader(seed), out); err != nil {
		panic(err)
	}
	return out
}
