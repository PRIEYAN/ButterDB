package buckets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/apply"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	testDeploymentID = "6pzyoxbfwcf6ubwcq5gpvjg5c4"
	testFingerprint  = "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"
	testUserID       = int64(777000123)
	testKeyID        = "437ad4b129840534"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stableRandomID(jobID, purpose string) (int64, error) {
	digest := sha256.Sum256([]byte(jobID + "\x00" + purpose))
	return int64(binary.LittleEndian.Uint64(digest[:8])) | 1, nil
}

type sqlChannels struct {
	db *sqlitedb.DB
}

func (c *sqlChannels) Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error) {
	states, err := c.query(ctx, `WHERE channel_id = ?1`, channelID)
	if err != nil || len(states) == 0 {
		return syncer.ChannelState{}, false, err
	}
	return states[0], true, nil
}

func (c *sqlChannels) Channels(ctx context.Context) ([]syncer.ChannelState, error) {
	return c.query(ctx, `WHERE ?1 = 0`, 0)
}

func (c *sqlChannels) query(ctx context.Context, where string, arg any) ([]syncer.ChannelState, error) {
	var out []syncer.ChannelState
	err := c.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT channel_id, kind, access_hash, title, state, last_synced_msg, pts,
initial_sync_done, needs_projection_rebuild, last_reconciled_at FROM channels `+where+` ORDER BY channel_id`, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var state syncer.ChannelState
			var kind, status string
			var done, rebuild int
			if err := rows.Scan(&state.ChannelID, &kind, &state.AccessHash, &state.Title, &status, &state.LastSyncedMsg,
				&state.Pts, &done, &rebuild, &state.LastReconciledAt); err != nil {
				return err
			}
			state.Kind = syncer.ChannelKind(kind)
			state.Deleted = status == "deleted"
			state.InitialSyncDone = done == 1
			state.NeedsProjectionRebuild = rebuild == 1
			out = append(out, state)
		}
		return rows.Err()
	})
	return out, err
}

func (c *sqlChannels) UpsertChannel(ctx context.Context, state syncer.ChannelState) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO channels (channel_id, kind, access_hash, title) VALUES (?1, ?2, ?3, ?4)
ON CONFLICT(channel_id) DO UPDATE SET access_hash = excluded.access_hash, title = excluded.title`,
			state.ChannelID, string(state.Kind), state.AccessHash, state.Title)
		return err
	})
}

func (c *sqlChannels) UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE channels SET access_hash = ?2 WHERE channel_id = ?1`, channelID, accessHash)
		return err
	})
}

func (c *sqlChannels) InitialSyncEmptyChannel(ctx context.Context, channelID int64) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		var logged int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1`, channelID).Scan(&logged); err != nil {
			return err
		}
		if logged > 0 {
			return syncer.ErrChannelNotEmpty
		}
		_, err := tx.ExecContext(ctx, `UPDATE channels SET initial_sync_done = 1 WHERE channel_id = ?1`, channelID)
		return err
	})
}

type gateLog struct {
	mu       sync.Mutex
	acquired []int64
}

func (g *gateLog) Acquire(_ context.Context, channelID int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acquired = append(g.acquired, channelID)
	return func() {}, nil
}

type countingWriter struct {
	inner    projection.Writer
	mu       sync.Mutex
	writes   int
	failNext int
}

func (w *countingWriter) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	w.mu.Lock()
	w.writes++
	fail := w.failNext > 0
	if fail {
		w.failNext--
	}
	w.mu.Unlock()
	if fail {
		return errors.New("injected projection write failure")
	}
	return w.inner.Write(ctx, fn)
}

type memState struct {
	mu       sync.Mutex
	state    RegistryState
	present  bool
	saves    int
	failNext int
}

func (m *memState) LoadRegistryState(context.Context) (RegistryState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.present, nil
}

func (m *memState) SaveRegistryState(_ context.Context, state RegistryState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext > 0 {
		m.failNext--
		return errors.New("injected state save failure")
	}
	m.saves++
	m.state, m.present = state, true
	return nil
}

type fakeKeys struct {
	mu       sync.Mutex
	present  map[int64]bool
	minted   int
	mintFail error
}

func (k *fakeKeys) MintEncryptionConfig(_ context.Context, bucketChannelID int64) (wire.Op, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.mintFail != nil {
		return wire.Op{}, k.mintFail
	}
	k.minted++
	return wire.Op{
		Type:            wire.OpEncConfig,
		BucketChannelID: bucketChannelID,
		KeyID:           testKeyID,
		WrappedKey:      make([]byte, 72),
		KeyCheck:        make([]byte, 61),
		KEKFingerprint:  testFingerprint,
	}, nil
}

func (k *fakeKeys) HasActiveKey(_ context.Context, bucketChannelID int64) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.present[bucketChannelID], nil
}

type fakeJobs struct {
	mu     sync.Mutex
	active map[int64]bool
}

func (j *fakeJobs) HasActiveJobs(_ context.Context, bucketChannelID int64) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.active[bucketChannelID], nil
}

type noGeneration struct{}

func (noGeneration) Advance(int64) {}

type counterReader struct {
	mu sync.Mutex
	n  byte
}

func (r *counterReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

type world struct {
	t        *testing.T
	ctx      context.Context
	db       *sqlitedb.DB
	clock    *clock.Fake
	network  *faketg.Network
	telegram *faketg.Client
	channels *sqlChannels
	gate     *gateLog
	writer   *countingWriter
	state    *memState
	keys     *fakeKeys
	jobs     *fakeJobs
	repo     *bucketrepo.Repository
	service  *Service
	provider *RegistryProvisioner
	deps     Dependencies
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	clk := clock.NewFake(time.Unix(1763000000, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("new network: %v", err)
	}
	telegram, err := network.AddAccount(faketg.AccountConfig{UserID: testUserID, FirstName: "Owner", Username: "owner"})
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: noGeneration{}})
	if err != nil {
		t.Fatalf("new apply registry: %v", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	repo, err := bucketrepo.New(db)
	if err != nil {
		t.Fatalf("new bucket repo: %v", err)
	}
	w := &world{
		t: t, ctx: ctx, db: db, clock: clk, network: network, telegram: telegram,
		channels: &sqlChannels{db: db}, gate: &gateLog{}, writer: &countingWriter{inner: db},
		state: &memState{}, keys: &fakeKeys{present: map[int64]bool{}}, jobs: &fakeJobs{active: map[int64]bool{}}, repo: repo,
	}
	publish := PublishDependencies{
		Channels: w.channels, Resolver: telegram, Messages: telegram, Sender: telegram, Projector: projector,
		Writer: w.writer, StableRandomID: stableRandomID, Clock: clk, Logger: discardLogger(), ActorUserID: testUserID,
	}
	w.deps = Dependencies{
		Publish: publish, Buckets: repo, Scanner: w.channels, Gate: w.gate, Creator: telegram, Keys: w.keys,
		KeyPresence: w.keys, Jobs: w.jobs, WriteGates: NewWriteGates(), Random: &counterReader{}, DeploymentID: testDeploymentID,
	}
	w.service, err = NewService(w.deps)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	w.provider, err = NewRegistryProvisioner(RegistryDependencies{
		Publish: publish, State: w.state, Scanner: w.channels, Gate: w.gate, Creator: telegram,
		DeploymentID: testDeploymentID, KeyFingerprint: testFingerprint,
	})
	if err != nil {
		t.Fatalf("new registry provisioner: %v", err)
	}
	return w
}

func (w *world) linkRegistry() RegistryState {
	w.t.Helper()
	state, err := w.provider.Ensure(w.ctx)
	if err != nil {
		w.t.Fatalf("ensure registry: %v", err)
	}
	return state
}

func (w *world) createBucket(name string, encryption string) bucket.Info {
	w.t.Helper()
	info, err := w.service.Create(w.ctx, bucket.Spec{Name: bucket.BucketName(name), Policy: bucket.PolicyPrivate, DefaultEncryption: encryption, CacheMaxAge: 3600})
	if err != nil {
		w.t.Fatalf("create bucket %q: %v", name, err)
	}
	return info
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	err := w.db.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(w.ctx, query, args...)
		return err
	})
	if err != nil {
		w.t.Fatalf("exec %q: %v", query, err)
	}
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	err := w.db.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(w.ctx, query, args...).Scan(&n)
	})
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (w *world) registryID() int64 {
	w.t.Helper()
	states, err := w.channels.Channels(w.ctx)
	if err != nil {
		w.t.Fatalf("channels: %v", err)
	}
	for _, state := range states {
		if state.Kind == syncer.KindRegistry {
			return state.ChannelID
		}
	}
	w.t.Fatalf("no registry channel recorded")
	return 0
}

func (w *world) registryTexts() []string {
	w.t.Helper()
	var texts []string
	for _, sent := range w.network.SentControls(w.registryID()) {
		texts = append(texts, sent.Text)
	}
	return texts
}

func opTypes(texts []string) []string {
	var types []string
	for _, text := range texts {
		op, err := wire.Parse(wire.ExtractHeaderLine(text))
		if err != nil {
			types = append(types, "invalid")
			continue
		}
		types = append(types, string(op.Type))
	}
	return types
}

func joinTypes(types []string) string {
	return strings.Join(types, ",")
}

func (w *world) requireReplayMatchesTelegram() {
	w.t.Helper()
	messages, err := w.network.Messages(w.registryID())
	if err != nil {
		w.t.Fatalf("registry messages: %v", err)
	}
	for _, msg := range messages {
		var recorded string
		err := w.db.Read(w.ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(w.ctx, `SELECT raw_header FROM replay_log WHERE channel_id = ?1 AND msg_id = ?2`, w.registryID(), msg.MsgID).Scan(&recorded)
		})
		if err != nil {
			w.t.Fatalf("replay row for message %d: %v", msg.MsgID, err)
		}
		if want := strings.TrimSpace(wire.ExtractHeaderLine(msg.Text)); recorded != want {
			w.t.Fatalf("message %d: projected header %q differs from the Telegram text %q", msg.MsgID, recorded, want)
		}
	}
}
