package difference

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
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
	registryChannel int64 = 1000
	bucketChannel   int64 = 2000
	otherBucket     int64 = 2001
	actorUser       int64 = 7
)

var (
	_ OpProjector = (*projlog.Projector)(nil)
	_ KeyRepairer = (*rebuild.Rebuilder)(nil)

	_ DifferenceReader = (*faketg.Client)(nil)
	_ Sleeper          = (*recordingSleeper)(nil)

	_ syncer.ChannelGate     = (*recordingGate)(nil)
	_ syncer.ChannelStore    = (*sqlChannels)(nil)
	_ syncer.SyncCursorStore = (*sqlChannels)(nil)
	_ syncer.DeletionStore   = (*sqlChannels)(nil)
	_ syncer.HistoryScanner  = (*stubScanner)(nil)
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingSleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *recordingSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = append(s.waits, wait)
	return ctx.Err()
}

func (s *recordingSleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

type recordingGate struct {
	mu       sync.Mutex
	acquired []int64
	released int
}

func (g *recordingGate) Acquire(_ context.Context, channelID int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acquired = append(g.acquired, channelID)
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.released++
	}, nil
}

type sqlChannels struct {
	db         *sqlitedb.DB
	failCursor error
}

func (c *sqlChannels) Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error) {
	var state syncer.ChannelState
	var kind, rowState string
	var done, rebuild int
	err := c.db.Reader().QueryRowContext(ctx, `
		SELECT channel_id, access_hash, kind, title, state, last_synced_msg, pts, initial_sync_done, needs_projection_rebuild, last_reconciled_at
		FROM channels WHERE channel_id = ?`, channelID).
		Scan(&state.ChannelID, &state.AccessHash, &kind, &state.Title, &rowState, &state.LastSyncedMsg, &state.Pts, &done, &rebuild, &state.LastReconciledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return syncer.ChannelState{}, false, nil
	}
	if err != nil {
		return syncer.ChannelState{}, false, err
	}
	state.Kind = syncer.ChannelKind(kind)
	state.Deleted = rowState == "deleted"
	state.InitialSyncDone = done == 1
	state.NeedsProjectionRebuild = rebuild == 1
	return state, true, nil
}

func (c *sqlChannels) Channels(context.Context) ([]syncer.ChannelState, error) {
	return nil, errors.ErrUnsupported
}

func (c *sqlChannels) UpsertChannel(context.Context, syncer.ChannelState) error {
	return errors.ErrUnsupported
}

func (c *sqlChannels) UpdateAccessHash(context.Context, int64, int64) error {
	return errors.ErrUnsupported
}

func (c *sqlChannels) SetSyncCursorTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	if c.failCursor != nil {
		return c.failCursor
	}
	_, err := tx.ExecContext(ctx, `UPDATE channels SET last_synced_msg = ?, pts = ? WHERE channel_id = ?`, lastSyncedMsg, pts, channelID)
	return err
}

func (c *sqlChannels) MarkInitialSyncDoneTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE channels SET initial_sync_done = 1, needs_projection_rebuild = 0, last_synced_msg = ?, pts = ? WHERE channel_id = ?`,
		lastSyncedMsg, pts, channelID)
	return err
}

func (c *sqlChannels) SetNeedsProjectionRebuild(context.Context, int64, bool) error {
	return errors.ErrUnsupported
}

func (c *sqlChannels) SetLastReconciledAt(context.Context, int64, int64) error {
	return errors.ErrUnsupported
}

func (c *sqlChannels) RecordDeletedTx(ctx context.Context, tx projection.Tx, channelID int64, msgIDs []int64, source syncer.DeletionSource, recordedAt int64) ([]string, error) {
	var removed []string
	for _, msgID := range msgIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?, ?, ?, ?)`,
			channelID, msgID, string(source), recordedAt); err != nil {
			return nil, err
		}
		keys, err := c.removeObjectsBackedBy(ctx, tx, channelID, msgID)
		if err != nil {
			return nil, err
		}
		removed = append(removed, keys...)
	}
	return removed, nil
}

func (c *sqlChannels) removeObjectsBackedBy(ctx context.Context, tx projection.Tx, channelID, msgID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT object_key FROM objects WHERE channel_id = ? AND (put_msg_id = ? OR body_msg_id = ?)`, channelID, msgID, msgID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

type stubScanner struct {
	mu            sync.Mutex
	ensureCalls   int
	fullCalls     int
	onScan        func(ctx context.Context, channelID int64) error
	failEnsureErr error
}

func (s *stubScanner) EnsureAuthoritative(ctx context.Context, channelID int64) error {
	s.mu.Lock()
	s.ensureCalls++
	s.mu.Unlock()
	if s.failEnsureErr != nil {
		return s.failEnsureErr
	}
	return s.scan(ctx, channelID)
}

func (s *stubScanner) FullScan(ctx context.Context, channelID int64) error {
	s.mu.Lock()
	s.fullCalls++
	s.mu.Unlock()
	return s.scan(ctx, channelID)
}

func (s *stubScanner) InitialSyncEmptyChannel(context.Context, int64) error {
	return errors.ErrUnsupported
}

func (s *stubScanner) scan(ctx context.Context, channelID int64) error {
	if s.onScan == nil {
		return nil
	}
	return s.onScan(ctx, channelID)
}

func (s *stubScanner) counts() (ensure, full int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureCalls, s.fullCalls
}

type countingRepairer struct {
	inner    *rebuild.Rebuilder
	mu       sync.Mutex
	checks   int
	repaired []string
}

func (r *countingRepairer) NeedsKeyRepair(ctx context.Context, tx projection.Tx, channelID int64, key string, msgID int64) (bool, error) {
	r.mu.Lock()
	r.checks++
	r.mu.Unlock()
	return r.inner.NeedsKeyRepair(ctx, tx, channelID, key, msgID)
}

func (r *countingRepairer) RepairKeyTx(ctx context.Context, tx projection.Tx, channelID int64, key string) (rebuild.Result, error) {
	r.mu.Lock()
	r.repaired = append(r.repaired, key)
	r.mu.Unlock()
	return r.inner.RepairKeyTx(ctx, tx, channelID, key)
}

func (r *countingRepairer) counts() (checks int, repaired []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checks, append([]string(nil), r.repaired...)
}

type recordingIngester struct {
	inner  syncer.OpIngester
	mu     sync.Mutex
	order  []int64
	failAt int64
}

func (r *recordingIngester) IngestTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error) {
	r.mu.Lock()
	r.order = append(r.order, obs.MsgID)
	failAt := r.failAt
	r.mu.Unlock()
	if failAt != 0 && obs.MsgID == failAt {
		return projection.Outcome{}, fmt.Errorf("test: injected ingest failure at %d", obs.MsgID)
	}
	return r.inner.IngestTx(ctx, tx, obs)
}

func (r *recordingIngester) setFailAt(msgID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failAt = msgID
}

func (r *recordingIngester) ingested() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.order...)
}

type reversingReader struct {
	inner DifferenceReader
}

func (r reversingReader) GetChannelDifference(ctx context.Context, peer tgclient.InputPeer, pts int, limit int) (tgclient.ChannelDifference, error) {
	diff, err := r.inner.GetChannelDifference(ctx, peer, pts, limit)
	if err != nil {
		return diff, err
	}
	for i, j := 0, len(diff.Messages)-1; i < j; i, j = i+1, j-1 {
		diff.Messages[i], diff.Messages[j] = diff.Messages[j], diff.Messages[i]
	}
	return diff, nil
}

type env struct {
	t         *testing.T
	ctx       context.Context
	logs      *bytes.Buffer
	db        *sqlitedb.DB
	net       *faketg.Network
	tg        *faketg.Client
	peer      tgclient.InputPeer
	channels  *sqlChannels
	gate      *recordingGate
	scanner   *stubScanner
	sleeper   *recordingSleeper
	projector *projlog.Projector
	repairs   *countingRepairer
	ingest    *recordingIngester
	reader    DifferenceReader
	deletions syncer.DeletionStore
	service   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: noGeneration{}})
	if err != nil {
		t.Fatalf("new apply registry: %v", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: logger})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	rebuilder, err := rebuild.New(rebuild.Dependencies{Registry: registry, Logger: logger})
	if err != nil {
		t.Fatalf("new rebuilder: %v", err)
	}
	network, err := faketg.NewNetwork(clock.NewFake(time.Unix(1700000000, 0)))
	if err != nil {
		t.Fatalf("new network: %v", err)
	}
	account, err := network.AddAccount(faketg.AccountConfig{UserID: actorUser})
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	for _, id := range []int64{bucketChannel, otherBucket} {
		if _, err := network.SeedChannel(id, "bucket", "", account); err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}
	peer, err := account.PeerOf(bucketChannel)
	if err != nil {
		t.Fatalf("peer: %v", err)
	}
	e := &env{
		t: t, ctx: ctx, logs: logs, db: db, net: network, tg: account, peer: peer,
		channels: &sqlChannels{db: db}, gate: &recordingGate{}, scanner: &stubScanner{}, sleeper: &recordingSleeper{},
		projector: projector, repairs: &countingRepairer{inner: rebuilder}, reader: account,
	}
	e.deletions = e.channels
	e.seedRegistry()
	e.scanner.onScan = e.scanAll
	e.build()
	return e
}

type noGeneration struct{}

func (noGeneration) Advance(int64) {}

func (e *env) dependencies() Dependencies {
	return Dependencies{
		Gate: e.gate, Channels: e.channels, Cursors: e.channels, Deletions: e.deletions, Scanner: e.scanner,
		Ingester: e.ingest, Differences: e.reader, Writer: e.db, Sleeper: e.sleeper,
		Clock: clock.NewFake(time.Unix(1700000500, 0)), Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
	}
}

func (e *env) build() {
	e.t.Helper()
	ingester, err := NewIngester(IngesterDependencies{Projector: e.projector, Repairs: e.repairs, Logger: discardLogger()})
	if err != nil {
		e.t.Fatalf("new ingester: %v", err)
	}
	e.ingest = &recordingIngester{inner: ingester}
	service, err := New(e.dependencies())
	if err != nil {
		e.t.Fatalf("new service: %v", err)
	}
	e.service = service
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, query, args...)
		return err
	})
	if err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Reader().QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) seedRegistry() {
	e.t.Helper()
	e.exec(`INSERT INTO channels (channel_id, kind, access_hash, initial_sync_done) VALUES (?, 'registry', 1, 1)`, registryChannel)
	for i, id := range []int64{bucketChannel, otherBucket} {
		op := wire.Op{
			Type: wire.OpBucketCreate, JobID: jobID(100 + i), Bucket: fmt.Sprintf("bucket-%d", id), BucketChannelID: id,
			Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600, Timestamp: 1700000000,
		}
		outcome, err := e.projector.ProjectFromOp(e.ctx, e.db, projection.Observed{
			ChannelID: registryChannel, MsgID: int64(10 + i), MsgDate: 1700000000, RawText: formatOp(e.t, op),
		})
		if err != nil || outcome.Kind != projection.OutcomeApplied {
			e.t.Fatalf("project bcreate: outcome %+v err %v", outcome, err)
		}
	}
	e.exec(`UPDATE channels SET access_hash = ? WHERE channel_id = ?`, e.peer.AccessHash, bucketChannel)
}

func (e *env) setCursor(lastSynced int64, pts int, authoritative bool) {
	e.t.Helper()
	done := 0
	if authoritative {
		done = 1
	}
	e.exec(`UPDATE channels SET last_synced_msg = ?, pts = ?, initial_sync_done = ? WHERE channel_id = ?`, lastSynced, pts, done, bucketChannel)
}

func (e *env) state() syncer.ChannelState {
	e.t.Helper()
	state, found, err := e.channels.Channel(e.ctx, bucketChannel)
	if err != nil || !found {
		e.t.Fatalf("channel state: found=%v err=%v", found, err)
	}
	return state
}

func (e *env) networkPts() int {
	e.t.Helper()
	pts, err := e.net.Pts(bucketChannel)
	if err != nil {
		e.t.Fatalf("network pts: %v", err)
	}
	return pts
}

func (e *env) seedOp(msgID int64, op wire.Op) {
	e.t.Helper()
	e.seedText(msgID, formatOp(e.t, op))
}

func (e *env) seedText(msgID int64, text string) {
	e.t.Helper()
	if err := e.net.SeedHistory(bucketChannel, tgclient.HistoryMessage{MsgID: msgID, FromID: actorUser, Text: text}); err != nil {
		e.t.Fatalf("seed message %d: %v", msgID, err)
	}
}

func (e *env) projectLocally(msgID int64, op wire.Op) projection.Outcome {
	e.t.Helper()
	outcome, err := e.projector.ProjectFromOp(e.ctx, e.db, projection.Observed{
		ChannelID: bucketChannel, MsgID: msgID, MsgDate: 1700000000, ActorUserID: actorUser, RawText: formatOp(e.t, op),
	})
	if err != nil {
		e.t.Fatalf("project locally %d: %v", msgID, err)
	}
	return outcome
}

func (e *env) scanAll(ctx context.Context, channelID int64) error {
	pts, err := e.tg.GetChannelPts(ctx, e.peer)
	if err != nil {
		return err
	}
	var history []tgclient.HistoryMessage
	for offset := int64(0); ; {
		page, err := e.tg.GetHistory(ctx, e.peer, offset, 100)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		history = append(history, page...)
		offset = page[len(page)-1].MsgID
	}
	sort.Slice(history, func(i, j int) bool { return history[i].MsgID < history[j].MsgID })
	var highest int64
	if len(history) > 0 {
		highest = history[len(history)-1].MsgID
	}
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		for _, message := range history {
			if _, err := e.ingest.IngestTx(ctx, tx, observed(channelID, message)); err != nil {
				return err
			}
		}
		return e.channels.MarkInitialSyncDoneTx(ctx, tx, channelID, highest, pts)
	})
}

func jobID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n)
}

func formatOp(t *testing.T, op wire.Op) string {
	t.Helper()
	text, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return text
}

func putOp(key string, bodyMsgID int64, job int) wire.Op {
	return wire.Op{
		Type: wire.OpPut, JobID: jobID(job), Key: key, BodyMsgID: bodyMsgID, StoredSize: 100, PlaintextSize: 100,
		ETag: "d41d8cd98f00b204e9800998ecf8427e", SHA256: wire.EmptySHA256, ContentType: "text/plain",
		Writer: "key.test", Timestamp: 1700000000,
	}
}

func delOp(key string, job int) wire.Op {
	return wire.Op{Type: wire.OpDel, JobID: jobID(job), Key: key, Writer: "key.test", Timestamp: 1700000000}
}

func (e *env) livePut(key string) int64 {
	e.t.Helper()
	return int64(e.count(`SELECT COALESCE((SELECT put_msg_id FROM objects WHERE channel_id = ? AND object_key = ?), 0)`, bucketChannel, key))
}
