package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

type memoryGate struct {
	mu    sync.Mutex
	locks map[int64]chan struct{}
}

func newMemoryGate() *memoryGate {
	return &memoryGate{locks: make(map[int64]chan struct{})}
}

func (g *memoryGate) slot(channelID int64) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.locks[channelID]
	if !ok {
		ch = make(chan struct{}, 1)
		g.locks[channelID] = ch
	}
	return ch
}

func (g *memoryGate) Acquire(ctx context.Context, channelID int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slot := g.slot(channelID)
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type sqlStores struct {
	db *sqlitedb.DB
}

func (s sqlStores) Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error) {
	var st syncer.ChannelState
	var kind, state string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT channel_id, access_hash, kind, title, state, last_reconciled_at FROM channels WHERE channel_id = ?1`, channelID).
			Scan(&st.ChannelID, &st.AccessHash, &kind, &st.Title, &state, &st.LastReconciledAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return syncer.ChannelState{}, false, nil
	}
	if err != nil {
		return syncer.ChannelState{}, false, err
	}
	st.Kind = syncer.ChannelKind(kind)
	st.Deleted = state == "deleted"
	return st, true, nil
}

func (s sqlStores) Channels(context.Context) ([]syncer.ChannelState, error) { return nil, nil }

func (s sqlStores) UpsertChannel(context.Context, syncer.ChannelState) error { return nil }

func (s sqlStores) UpdateAccessHash(context.Context, int64, int64) error { return nil }

func (s sqlStores) LiveMessageIDs(ctx context.Context, channelID int64) ([]syncer.LiveObjectRef, error) {
	var refs []syncer.LiveObjectRef
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT object_key, put_msg_id, body_msg_id FROM objects WHERE channel_id = ?1 ORDER BY put_msg_id`, channelID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref syncer.LiveObjectRef
			var body int64
			if err := rows.Scan(&ref.Key, &ref.PutMsgID, &body); err != nil {
				return err
			}
			ref.BackingMsgIDs = []int64{ref.PutMsgID}
			if body > 0 {
				ref.BackingMsgIDs = append(ref.BackingMsgIDs, body)
			}
			refs = append(refs, ref)
		}
		return rows.Err()
	})
	return refs, err
}

func (s sqlStores) RecordDeletedTx(ctx context.Context, tx projection.Tx, channelID int64, msgIDs []int64, source syncer.DeletionSource, at int64) ([]string, error) {
	var removed []string
	for _, id := range msgIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, ?2, ?3, ?4)`,
			channelID, id, string(source), at); err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx,
			`DELETE FROM objects WHERE channel_id = ?1 AND (put_msg_id = ?2 OR body_msg_id = ?2) RETURNING object_key`, channelID, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return nil, err
			}
			removed = append(removed, key)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

type sqlCursors struct {
	syncer.SyncCursorStore
	db *sqlitedb.DB
}

func (c sqlCursors) SetLastReconciledAt(ctx context.Context, channelID, at int64) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE channels SET last_reconciled_at = ?2 WHERE channel_id = ?1`, channelID, at)
		return err
	})
}

type countingChecker struct {
	inner      MessageChecker
	mu         sync.Mutex
	batches    [][]int64
	placeholds map[int64]bool
}

func (c *countingChecker) GetMessages(ctx context.Context, peer tgclient.InputPeer, ids []int64) ([]tgclient.HistoryMessage, error) {
	c.mu.Lock()
	c.batches = append(c.batches, slices.Clone(ids))
	c.mu.Unlock()
	msgs, err := c.inner.GetMessages(ctx, peer, ids)
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if c.placeholds[msgs[i].MsgID] {
			msgs[i].Placeholder = true
		}
	}
	return msgs, nil
}

func (c *countingChecker) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

type env struct {
	world   *fakeWorld
	db      *sqlitedb.DB
	stores  sqlStores
	gate    *memoryGate
	checker *countingChecker
	deps    Dependencies
	rec     *Reconciler
	peer    tgclient.InputPeer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	w := newWorld(t)
	peer := w.channel(t, "butterDB bucket photos")
	e := &env{world: w, db: db, stores: sqlStores{db: db}, gate: newMemoryGate(), peer: peer}
	e.checker = &countingChecker{inner: w.alice, placeholds: map[int64]bool{}}
	e.deps = Dependencies{
		Gate: e.gate, Channels: e.stores, Live: e.stores, Deletions: e.stores,
		Cursors: sqlCursors{db: db}, Telegram: e.checker, Writer: db, Clock: w.clock, Logger: discardLogger(),
	}
	e.rec = e.build(t)
	e.exec(t, `INSERT INTO channels (channel_id, kind, access_hash, title) VALUES (?1, 'bucket', ?2, 'butterDB bucket photos')`, peer.ChannelID, peer.AccessHash)
	return e
}

func (e *env) build(t *testing.T) *Reconciler {
	t.Helper()
	r, err := NewReconciler(e.deps)
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	return r
}

func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) addObject(t *testing.T, key string) (bodyID, putID int64) {
	t.Helper()
	bodyID = e.world.say(t, e.peer, "body "+key)
	putID = e.world.say(t, e.peer, "put "+key)
	e.exec(t, `INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256, content_type, encrypted, writer, op_ts, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, 10, 10, ?6, ?7, 'text/plain', 0, 'w', 1, 1)`,
		e.peer.ChannelID, key, putID, bodyID, testJobID, strings.Repeat("a", 32), strings.Repeat("b", 64))
	return bodyID, putID
}

func (e *env) deleteOnTelegram(t *testing.T, ids ...int64) {
	t.Helper()
	if err := e.world.alice.DeleteMessages(context.Background(), e.peer, ids); err != nil {
		t.Fatalf("delete on telegram: %v", err)
	}
}

func (e *env) liveKeys(t *testing.T) []string {
	t.Helper()
	refs, err := e.stores.LiveMessageIDs(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("live objects: %v", err)
	}
	var keys []string
	for _, ref := range refs {
		keys = append(keys, ref.Key)
	}
	return keys
}

func (e *env) deleted(t *testing.T) map[int64]string {
	t.Helper()
	out := map[int64]string{}
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT msg_id, source FROM deleted_messages WHERE channel_id = ?1`, e.peer.ChannelID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var source string
			if err := rows.Scan(&id, &source); err != nil {
				return err
			}
			out[id] = source
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read deleted_messages: %v", err)
	}
	return out
}

func (e *env) lastReconciled(t *testing.T) int64 {
	t.Helper()
	st, ok, err := e.stores.Channel(context.Background(), e.peer.ChannelID)
	if err != nil || !ok {
		t.Fatalf("load channel: %v %v", ok, err)
	}
	return st.LastReconciledAt
}

func TestReconcileDeletionsRemovesObjectWithDeletedMessage(t *testing.T) {
	tests := []struct {
		name    string
		deleted func(body, put int64) []int64
		wantRec func(body, put int64) map[int64]string
	}{
		{name: "put deleted", deleted: func(_, put int64) []int64 { return []int64{put} }, wantRec: func(_, put int64) map[int64]string { return map[int64]string{put: "reconcile"} }},
		{name: "both deleted", deleted: func(body, put int64) []int64 { return []int64{body, put} }, wantRec: func(body, put int64) map[int64]string {
			return map[int64]string{body: "reconcile", put: "reconcile"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body, put := e.addObject(t, "doomed.txt")
			e.addObject(t, "safe.txt")
			e.deleteOnTelegram(t, tt.deleted(body, put)...)

			res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if !slices.Equal(res.RemovedKeys, []string{"doomed.txt"}) {
				t.Fatalf("removed = %v, want [doomed.txt]", res.RemovedKeys)
			}
			if got := e.liveKeys(t); !slices.Equal(got, []string{"safe.txt"}) {
				t.Fatalf("live keys = %v, want [safe.txt]", got)
			}
			got := e.deleted(t)
			want := tt.wantRec(body, put)
			if len(got) != len(want) {
				t.Fatalf("deleted_messages = %v, want %v", got, want)
			}
			for id, src := range want {
				if got[id] != src {
					t.Fatalf("deleted_messages = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestMissingBodyRemovesObject(t *testing.T) {
	e := newEnv(t)
	body, put := e.addObject(t, "report.pdf")
	e.deleteOnTelegram(t, body)

	res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !slices.Equal(res.MissingMsgIDs, []int64{body}) || !slices.Equal(res.RemovedKeys, []string{"report.pdf"}) {
		t.Fatalf("result = %+v, want body %d missing and report.pdf removed", res, body)
	}
	if got := e.liveKeys(t); len(got) != 0 {
		t.Fatalf("object survived a missing body: %v", got)
	}
	if got := e.deleted(t); len(got) != 1 || got[body] != "reconcile" {
		t.Fatalf("deleted_messages = %v, want only the body as reconcile (put %d still exists)", got, put)
	}
	if got := e.lastReconciled(t); got != e.world.clock.Now().Unix() {
		t.Fatalf("last_reconciled_at = %d, want %d", got, e.world.clock.Now().Unix())
	}
}

func TestReconcileDeletionsLeavesLiveObjectsAlone(t *testing.T) {
	e := newEnv(t)
	e.addObject(t, "a.txt")
	e.addObject(t, "b.txt")

	res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Checked != 4 || len(res.MissingMsgIDs) != 0 || len(res.RemovedKeys) != 0 {
		t.Fatalf("result = %+v, want 4 checked and nothing missing", res)
	}
	if got := e.liveKeys(t); len(got) != 2 {
		t.Fatalf("live keys = %v, want both objects", got)
	}
	if got := e.deleted(t); len(got) != 0 {
		t.Fatalf("deleted_messages = %v, want none", got)
	}
	if e.lastReconciled(t) == 0 {
		t.Fatal("a completed pass did not stamp last_reconciled_at")
	}
}

func TestReconcileDeletionsEmptyChannelNoop(t *testing.T) {
	e := newEnv(t)
	res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Checked != 0 || len(res.MissingMsgIDs) != 0 || res.Skipped {
		t.Fatalf("result = %+v, want a no-op", res)
	}
	if e.checker.calls() != 0 {
		t.Fatalf("telegram was called %d times for an empty channel", e.checker.calls())
	}
	if e.lastReconciled(t) != 0 {
		t.Fatal("an empty pass stamped last_reconciled_at")
	}
}

func TestReconcileBatches(t *testing.T) {
	e := newEnv(t)
	const objects = 250
	var bodies []int64
	for i := 0; i < objects; i++ {
		body, _ := e.addObject(t, fmt.Sprintf("k%03d", i))
		bodies = append(bodies, body)
	}
	gone := []int64{bodies[0], bodies[120], bodies[249]}
	e.deleteOnTelegram(t, gone...)

	res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Checked != 2*objects {
		t.Fatalf("checked = %d, want %d", res.Checked, 2*objects)
	}
	if got := len(e.checker.batches); got != 5 {
		t.Fatalf("existence calls = %d, want 5", got)
	}
	var total int
	for i, batch := range e.checker.batches {
		if len(batch) > limits.DeleteBatchSize {
			t.Fatalf("batch %d has %d ids, limit %d", i, len(batch), limits.DeleteBatchSize)
		}
		total += len(batch)
	}
	if total != 2*objects {
		t.Fatalf("ids over all batches = %d, want %d", total, 2*objects)
	}
	if !slices.Equal(res.MissingMsgIDs, gone) {
		t.Fatalf("missing = %v, want %v", res.MissingMsgIDs, gone)
	}
	if got := len(e.liveKeys(t)); got != objects-3 {
		t.Fatalf("live objects = %d, want %d", got, objects-3)
	}
	if got := len(e.deleted(t)); got != 3 {
		t.Fatalf("deleted_messages rows = %d, want 3", got)
	}
}

func TestReconcileDeletionsNeverActsOnError(t *testing.T) {
	e := newEnv(t)
	var bodies []int64
	for i := 0; i < 120; i++ {
		body, _ := e.addObject(t, fmt.Sprintf("k%03d", i))
		bodies = append(bodies, body)
	}
	e.deleteOnTelegram(t, bodies[0])
	boom := errors.New("transport down")
	if err := e.world.alice.FaultOnCall(faketg.MethodGetMessages, 2, faketg.Fault{Kind: faketg.FaultError, Err: boom}); err != nil {
		t.Fatalf("fault: %v", err)
	}

	_, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if !errors.Is(err, boom) {
		t.Fatalf("reconcile = %v, want transport error", err)
	}
	if got := len(e.liveKeys(t)); got != 120 {
		t.Fatalf("live objects = %d after an error, want all 120", got)
	}
	if got := e.deleted(t); len(got) != 0 {
		t.Fatalf("deleted_messages = %v after an error, want none", got)
	}
	if e.lastReconciled(t) != 0 {
		t.Fatal("a failed pass stamped last_reconciled_at")
	}
}

func TestReconcileDeletionsPlaceholderCountsAsMissing(t *testing.T) {
	e := newEnv(t)
	body, _ := e.addObject(t, "empty.txt")
	e.checker.placeholds[body] = true

	res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !slices.Equal(res.RemovedKeys, []string{"empty.txt"}) {
		t.Fatalf("removed = %v, want [empty.txt]", res.RemovedKeys)
	}
}

func TestReconcileDeletionsRunsAtMostOncePerDay(t *testing.T) {
	e := newEnv(t)
	e.addObject(t, "a.txt")
	ctx := context.Background()
	if _, err := e.rec.ReconcileDeletions(ctx, e.peer.ChannelID); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	callsAfterFirst := e.checker.calls()

	e.world.clock.Advance(24*time.Hour - time.Second)
	res, err := e.rec.ReconcileDeletions(ctx, e.peer.ChannelID)
	if err != nil || !res.Skipped {
		t.Fatalf("pass inside 24h = %+v %v, want skipped", res, err)
	}
	if e.checker.calls() != callsAfterFirst {
		t.Fatal("a skipped pass called telegram")
	}

	e.world.clock.Advance(time.Second)
	res, err = e.rec.ReconcileDeletions(ctx, e.peer.ChannelID)
	if err != nil || res.Skipped || res.Checked != 2 {
		t.Fatalf("pass after 24h = %+v %v, want a full check", res, err)
	}
	if e.lastReconciled(t) != e.world.clock.Now().Unix() {
		t.Fatalf("last_reconciled_at = %d, want %d", e.lastReconciled(t), e.world.clock.Now().Unix())
	}
}

func TestReconcileDeletionsChannelStates(t *testing.T) {
	t.Run("unknown channel", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.rec.ReconcileDeletions(context.Background(), 424242); !errors.Is(err, syncer.ErrUnknownChannel) {
			t.Fatalf("reconcile = %v, want ErrUnknownChannel", err)
		}
	})
	t.Run("deleted channel is skipped", func(t *testing.T) {
		e := newEnv(t)
		e.addObject(t, "a.txt")
		e.exec(t, `UPDATE channels SET state = 'deleted' WHERE channel_id = ?1`, e.peer.ChannelID)
		res, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID)
		if err != nil || !res.Skipped || e.checker.calls() != 0 {
			t.Fatalf("reconcile = %+v %v calls %d, want skipped without telegram", res, err, e.checker.calls())
		}
	})
	t.Run("zero access hash", func(t *testing.T) {
		e := newEnv(t)
		e.addObject(t, "a.txt")
		e.exec(t, `UPDATE channels SET access_hash = 0 WHERE channel_id = ?1`, e.peer.ChannelID)
		if _, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID); !errors.Is(err, tgclient.ErrZeroAccessHash) {
			t.Fatalf("reconcile = %v, want ErrZeroAccessHash", err)
		}
		if e.checker.calls() != 0 {
			t.Fatal("telegram was called without an access hash")
		}
	})
}

func TestReconcileDeletionsWaitingCallerCancels(t *testing.T) {
	e := newEnv(t)
	e.addObject(t, "a.txt")
	release, err := e.gate.Acquire(context.Background(), e.peer.ChannelID)
	if err != nil {
		t.Fatalf("hold gate: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := e.rec.ReconcileDeletions(ctx, e.peer.ChannelID)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting reconcile = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled reconcile kept waiting for the gate")
	}
	if e.checker.calls() != 0 {
		t.Fatal("a reconcile that never held the gate called telegram")
	}
}

func TestReconcileDeletionsReleasesGate(t *testing.T) {
	e := newEnv(t)
	e.addObject(t, "a.txt")
	if _, err := e.rec.ReconcileDeletions(context.Background(), e.peer.ChannelID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := e.gate.Acquire(ctx, e.peer.ChannelID)
	if err != nil {
		t.Fatalf("gate still held after reconcile: %v", err)
	}
	release()
}

type failingDeletions struct {
	syncer.DeletionStore
}

func (failingDeletions) RecordDeletedTx(context.Context, projection.Tx, int64, []int64, syncer.DeletionSource, int64) ([]string, error) {
	return nil, errors.New("disk full")
}

func TestReconcileDeletionsRecordFailureLeavesNoStamp(t *testing.T) {
	e := newEnv(t)
	body, _ := e.addObject(t, "a.txt")
	e.deleteOnTelegram(t, body)
	e.deps.Deletions = failingDeletions{}
	rec := e.build(t)
	if _, err := rec.ReconcileDeletions(context.Background(), e.peer.ChannelID); err == nil {
		t.Fatal("reconcile succeeded although recording failed")
	}
	if e.lastReconciled(t) != 0 {
		t.Fatal("a pass that failed to record stamped last_reconciled_at")
	}
	if got := e.liveKeys(t); len(got) != 1 {
		t.Fatalf("live keys = %v, want the object untouched", got)
	}
}

func TestNewReconcilerRejectsMissingDependencies(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{name: "gate", mutate: func(d *Dependencies) { d.Gate = nil }},
		{name: "channels", mutate: func(d *Dependencies) { d.Channels = nil }},
		{name: "live objects", mutate: func(d *Dependencies) { d.Live = nil }},
		{name: "deletions", mutate: func(d *Dependencies) { d.Deletions = nil }},
		{name: "cursors", mutate: func(d *Dependencies) { d.Cursors = nil }},
		{name: "telegram", mutate: func(d *Dependencies) { d.Telegram = nil }},
		{name: "writer", mutate: func(d *Dependencies) { d.Writer = nil }},
		{name: "clock", mutate: func(d *Dependencies) { d.Clock = nil }},
		{name: "logger", mutate: func(d *Dependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := e.deps
			tt.mutate(&deps)
			if _, err := NewReconciler(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewReconciler = %v, want ErrMissingDependency", err)
			}
		})
	}
}
