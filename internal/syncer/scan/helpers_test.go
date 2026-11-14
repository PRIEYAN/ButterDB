package scan

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/apply"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	registryID = int64(1000)
	bucketID   = int64(500)
	userID     = int64(7)
)

type noopGeneration struct{}

func (noopGeneration) Advance(int64) {}

type fakeSleeper struct {
	mu    sync.Mutex
	waits []time.Duration
	hook  func(ctx context.Context, wait time.Duration) error
}

func (f *fakeSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	f.mu.Lock()
	f.waits = append(f.waits, wait)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, wait)
	}
	return ctx.Err()
}

func (f *fakeSleeper) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type sqlStores struct {
	db *sqlitedb.DB
}

var (
	_ syncer.ChannelStore    = (*sqlStores)(nil)
	_ syncer.SyncCursorStore = (*sqlStores)(nil)
	_ syncer.ScanSpool       = (*sqlStores)(nil)
)

const channelColumns = `channel_id, kind, access_hash, title, state, last_synced_msg, pts, initial_sync_done, needs_projection_rebuild, last_reconciled_at`

func scanChannel(row interface{ Scan(...any) error }) (syncer.ChannelState, error) {
	var state syncer.ChannelState
	var kind, status string
	var done, rebuild int
	err := row.Scan(&state.ChannelID, &kind, &state.AccessHash, &state.Title, &status, &state.LastSyncedMsg,
		&state.Pts, &done, &rebuild, &state.LastReconciledAt)
	state.Kind = syncer.ChannelKind(kind)
	state.Deleted = status == "deleted"
	state.InitialSyncDone = done == 1
	state.NeedsProjectionRebuild = rebuild == 1
	return state, err
}

func (s *sqlStores) Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error) {
	var state syncer.ChannelState
	found := true
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = scanChannel(tx.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE channel_id = ?`, channelID))
		if err == sql.ErrNoRows {
			found = false
			return nil
		}
		return err
	})
	return state, found, err
}

func (s *sqlStores) Channels(ctx context.Context) ([]syncer.ChannelState, error) {
	var out []syncer.ChannelState
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+channelColumns+` FROM channels ORDER BY channel_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			state, err := scanChannel(rows)
			if err != nil {
				return err
			}
			out = append(out, state)
		}
		return rows.Err()
	})
	return out, err
}

func (s *sqlStores) UpsertChannel(ctx context.Context, state syncer.ChannelState) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO channels (channel_id, kind, access_hash, title) VALUES (?1, ?2, ?3, ?4)
ON CONFLICT (channel_id) DO UPDATE SET access_hash = ?3, title = ?4`, state.ChannelID, string(state.Kind), state.AccessHash, state.Title)
		return err
	})
}

func (s *sqlStores) UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error {
	return s.exec(ctx, `UPDATE channels SET access_hash = ? WHERE channel_id = ?`, accessHash, channelID)
}

func (s *sqlStores) exec(ctx context.Context, query string, args ...any) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

func (s *sqlStores) SetSyncCursorTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	_, err := tx.ExecContext(ctx, `UPDATE channels SET last_synced_msg = ?, pts = ? WHERE channel_id = ?`, lastSyncedMsg, pts, channelID)
	return err
}

func (s *sqlStores) MarkInitialSyncDoneTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	_, err := tx.ExecContext(ctx, `UPDATE channels SET last_synced_msg = ?, pts = ?, initial_sync_done = 1 WHERE channel_id = ?`, lastSyncedMsg, pts, channelID)
	return err
}

func (s *sqlStores) SetNeedsProjectionRebuild(ctx context.Context, channelID int64, needed bool) error {
	return s.exec(ctx, `UPDATE channels SET needs_projection_rebuild = ? WHERE channel_id = ?`, boolInt(needed), channelID)
}

func (s *sqlStores) SetLastReconciledAt(ctx context.Context, channelID, at int64) error {
	return s.exec(ctx, `UPDATE channels SET last_reconciled_at = ? WHERE channel_id = ?`, at, channelID)
}

func (s *sqlStores) ClearSpool(ctx context.Context, channelID int64) error {
	return s.exec(ctx, `DELETE FROM initial_scan_spool WHERE channel_id = ?`, channelID)
}

func (s *sqlStores) SpoolPage(ctx context.Context, channelID int64, msgs []tgclient.HistoryMessage) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, m := range msgs {
			_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO initial_scan_spool
(channel_id, msg_id, from_id, date, text, has_media, media_size, document_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				channelID, m.MsgID, m.FromID, m.Date, m.Text, boolInt(m.HasMedia), m.MediaSize, m.DocumentName)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *sqlStores) SpooledPageTx(ctx context.Context, tx projection.Tx, channelID, afterMsgID int64, limit int) ([]tgclient.HistoryMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT msg_id, from_id, date, text, has_media, media_size, document_name
FROM initial_scan_spool WHERE channel_id = ? AND msg_id > ? ORDER BY msg_id ASC LIMIT ?`, channelID, afterMsgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tgclient.HistoryMessage
	for rows.Next() {
		var m tgclient.HistoryMessage
		var media int
		if err := rows.Scan(&m.MsgID, &m.FromID, &m.Date, &m.Text, &media, &m.MediaSize, &m.DocumentName); err != nil {
			return nil, err
		}
		m.HasMedia = media == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type projectorIngester struct {
	projector *projlog.Projector
}

func (p projectorIngester) IngestTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error) {
	return p.projector.ProjectFromOpTx(ctx, tx, obs)
}

type clientPeers struct {
	client *faketg.Client
}

func (c clientPeers) ResolvePeer(_ context.Context, channelID int64) (tgclient.InputPeer, error) {
	return c.client.PeerOf(channelID)
}

type env struct {
	t       *testing.T
	db      *sqlitedb.DB
	net     *faketg.Network
	client  *faketg.Client
	stores  *sqlStores
	sleeper *fakeSleeper
	gate    *Gate
	scanner *Scanner
	ingest  projectorIngester
}

type envOptions struct {
	history  func(base HistoryReader) HistoryReader
	ingester func(base syncer.OpIngester) syncer.OpIngester
}

type envOption func(*envOptions)

func withHistory(wrap func(base HistoryReader) HistoryReader) envOption {
	return func(o *envOptions) { o.history = wrap }
}

func withIngester(wrap func(base syncer.OpIngester) syncer.OpIngester) envOption {
	return func(o *envOptions) { o.ingester = wrap }
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	var options envOptions
	for _, opt := range opts {
		opt(&options)
	}
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: noopGeneration{}})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	rebuilder, err := rebuild.New(rebuild.Dependencies{Registry: registry, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new rebuilder: %v", err)
	}
	fakeClock := clock.NewFake(time.Unix(1700000000, 0))
	network, err := faketg.NewNetwork(fakeClock)
	if err != nil {
		t.Fatalf("new network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: userID, FirstName: "Test", Username: "test"})
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	e := &env{t: t, db: db, net: network, client: client, stores: &sqlStores{db: db}, sleeper: &fakeSleeper{}, gate: NewGate(),
		ingest: projectorIngester{projector: projector}}
	var history HistoryReader = client
	if options.history != nil {
		history = options.history(client)
	}
	var ingester syncer.OpIngester = e.ingest
	if options.ingester != nil {
		ingester = options.ingester(e.ingest)
	}
	e.scanner, err = NewScanner(Dependencies{
		Channels: e.stores, Cursors: e.stores, Spool: e.stores, Ingester: ingester, Rebuilder: rebuilder,
		Writer: db, Peers: clientPeers{client: client}, History: history, Gate: e.gate, Sleeper: e.sleeper,
		Clock: fakeClock, Random: zeroReader{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	e.addChannel(registryID, syncer.KindRegistry)
	return e
}

func (e *env) addChannel(channelID int64, kind syncer.ChannelKind) {
	e.t.Helper()
	if _, err := e.net.SeedChannel(channelID, "channel", "", e.client); err != nil {
		e.t.Fatalf("seed channel %d: %v", channelID, err)
	}
	if err := e.stores.UpsertChannel(context.Background(), syncer.ChannelState{ChannelID: channelID, Kind: kind, AccessHash: 1}); err != nil {
		e.t.Fatalf("upsert channel %d: %v", channelID, err)
	}
}

func (e *env) state(channelID int64) syncer.ChannelState {
	e.t.Helper()
	state, found, err := e.stores.Channel(context.Background(), channelID)
	if err != nil || !found {
		e.t.Fatalf("channel %d: found=%v err=%v", channelID, found, err)
	}
	return state
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.stores.exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) seed(channelID int64, text string) int64 {
	e.t.Helper()
	if err := e.net.SeedHistory(channelID, tgclient.HistoryMessage{Text: text}); err != nil {
		e.t.Fatalf("seed history: %v", err)
	}
	msgs, err := e.net.Messages(channelID)
	if err != nil {
		e.t.Fatal(err)
	}
	return msgs[len(msgs)-1].MsgID
}

func (e *env) seedAt(channelID, msgID int64, text string) {
	e.t.Helper()
	if err := e.net.SeedHistory(channelID, tgclient.HistoryMessage{MsgID: msgID, Text: text}); err != nil {
		e.t.Fatalf("seed history at %d: %v", msgID, err)
	}
}

func (e *env) project(channelID, msgID int64, text string) projection.Outcome {
	e.t.Helper()
	var outcome projection.Outcome
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		outcome, err = e.ingest.IngestTx(context.Background(), tx, projection.Observed{ChannelID: channelID, MsgID: msgID, MsgDate: 1700000000, RawText: text})
		return err
	})
	if err != nil {
		e.t.Fatalf("project msg %d: %v", msgID, err)
	}
	return outcome
}

func (e *env) bucketPolicy(channelID int64) (string, bool) {
	e.t.Helper()
	var policy string
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT policy FROM buckets WHERE channel_id = ?`, channelID).Scan(&policy)
	})
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		e.t.Fatalf("bucket policy: %v", err)
	}
	return policy, true
}

func (e *env) channelEmpty() bool {
	e.t.Helper()
	var empty bool
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		empty, err = projlog.ChannelIsEmpty(context.Background(), tx, registryID)
		return err
	})
	if err != nil {
		e.t.Fatalf("channel is empty: %v", err)
	}
	return empty
}

func jobID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

func bucketName(n int) string {
	return fmt.Sprintf("bkt-%03d", n)
}

func bucketCreateText(t *testing.T, n int) string {
	t.Helper()
	return formatOp(t, wire.Op{Type: wire.OpBucketCreate, JobID: jobID(n), Bucket: bucketName(n), BucketChannelID: int64(5000 + n),
		Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600, Timestamp: 1700000000})
}

func bucketConfigText(t *testing.T, n int, policy string, job int) string {
	t.Helper()
	return formatOp(t, wire.Op{Type: wire.OpBucketConfig, JobID: jobID(job), Bucket: bucketName(n), BucketChannelID: int64(5000 + n),
		Policy: policy, DefaultEncryption: "none", CacheMaxAge: 3600, Timestamp: 1700000001})
}

func formatOp(t *testing.T, op wire.Op) string {
	t.Helper()
	text, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return text
}

func (e *env) addChannelRowOnly(channelID int64, kind syncer.ChannelKind) {
	e.t.Helper()
	if err := e.stores.UpsertChannel(context.Background(), syncer.ChannelState{ChannelID: channelID, Kind: kind, AccessHash: 1}); err != nil {
		e.t.Fatalf("upsert channel %d: %v", channelID, err)
	}
}
