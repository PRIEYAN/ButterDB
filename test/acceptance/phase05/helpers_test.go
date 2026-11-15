package phase05

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	primaryUser = int64(777000123)
	baseTime    = int64(1763000000)
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
	sync       *core.Sync
	deployment string
	logs       *logBuffer
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
		activity: conn.NewActivity(0), random: newSeqReader("job randomness"), logs: &logBuffer{}}
	w.control = w.open(sqlitedb.Control, schema.Control, "control.db")
	w.openProjection()
	w.provisionRegistry()
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

func (w *world) openProjection() {
	w.t.Helper()
	w.db = w.open(sqlitedb.Projection, schema.Projection, "projection.db")
	projection, err := core.NewProjection(core.ProjectionDependencies{Logger: discardLogger()})
	if err != nil {
		w.t.Fatalf("projection: %v", err)
	}
	w.projection = projection
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
	w.buildSync()
}

func (w *world) buildSync() {
	w.t.Helper()
	state, err := core.NewRegistryStateStore(w.statePath(), w.writer, w.control, w.random)
	if err != nil {
		w.t.Fatalf("state store: %v", err)
	}
	built, err := core.NewSync(core.SyncDependencies{
		Database: w.db, Projection: w.projection, Telegram: w.client, Sender: w.client, Signals: w.activity,
		Ring: w.ring, State: state, ActorUserID: primaryUser, DeploymentID: w.deployment, Clock: w.clk,
		Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: slog.New(slog.NewTextHandler(w.logs, nil)),
	})
	if err != nil {
		w.t.Fatalf("new sync: %v", err)
	}
	w.sync = built
}

func (w *world) registryID() int64 {
	w.t.Helper()
	var id int64
	err := w.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT channel_id FROM channels WHERE kind = 'registry'`).Scan(&id)
	})
	if err != nil {
		w.t.Fatalf("registry channel: %v", err)
	}
	return id
}

func (w *world) peer(channelID int64) tgclient.InputPeer {
	w.t.Helper()
	peer, err := w.client.PeerOf(channelID)
	if err != nil {
		w.t.Fatalf("peer of %d: %v", channelID, err)
	}
	return peer
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	err := w.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (w *world) rows(query string, args ...any) []string {
	w.t.Helper()
	var out []string
	err := w.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				return err
			}
			parts := make([]string, len(values))
			for i, value := range values {
				parts[i] = fmt.Sprint(value)
			}
			out = append(out, strings.Join(parts, "|"))
		}
		return rows.Err()
	})
	if err != nil {
		w.t.Fatalf("rows %q: %v", query, err)
	}
	return out
}

func (w *world) sendExternal(channelID int64, text string) int64 {
	w.t.Helper()
	randomID := int64(len(text))*7919 + channelID + 1
	msgID, err := w.client.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{
		Peer: w.peer(channelID), Text: text, RandomID: randomID | 1,
	})
	if err != nil || msgID <= 0 {
		w.t.Fatalf("external send: %d, %v", msgID, err)
	}
	return msgID
}

func format(t *testing.T, op wire.Op) string {
	t.Helper()
	text, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return text
}

func putOp(key string, bodyMsgID int64, job string) wire.Op {
	return wire.Op{
		Type: wire.OpPut, JobID: job, Key: key, BodyMsgID: bodyMsgID, StoredSize: 100, PlaintextSize: 100,
		ETag: "d41d8cd98f00b204e9800998ecf8427e", SHA256: wire.EmptySHA256, ContentType: "text/plain",
		Writer: "key.external", Timestamp: baseTime,
	}
}

func delOp(key, job string) wire.Op {
	return wire.Op{Type: wire.OpDel, JobID: job, Key: key, Writer: "key.external", Timestamp: baseTime}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func (w *world) channelState(channelID int64) syncer.ChannelState {
	w.t.Helper()
	state, found, err := w.sync.Channels.Channel(context.Background(), channelID)
	if err != nil || !found {
		w.t.Fatalf("channel %d: found=%v err=%v", channelID, found, err)
	}
	return state
}

func (w *world) stateStore() buckets.RegistryStateStore {
	w.t.Helper()
	store, err := core.NewRegistryStateStore(w.statePath(), w.writer, w.control, w.random)
	if err != nil {
		w.t.Fatalf("state store: %v", err)
	}
	return store
}
