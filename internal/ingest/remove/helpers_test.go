package remove

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/apply"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

var (
	_ BucketLookup       = (*bucketrepo.Repository)(nil)
	_ WriteGates         = (*buckets.WriteGates)(nil)
	_ Authority          = (*buckets.Service)(nil)
	_ ObjectReader       = (*objectrepo.Repository)(nil)
	_ ControlSender      = (tgclient.Sender)(nil)
	_ MessageDeleter     = (tgclient.ChannelRPC)(nil)
	_ OpProjector        = (*projlog.Projector)(nil)
	_ StableRandomIDFunc = send.StableRandomID
)

const (
	registryChannel = int64(1000)
	testAccessKey   = "key.test"
	bucketName      = "photos"
)

type memJobs struct {
	mu          sync.Mutex
	jobs        map[string]jobrepo.DeleteJob
	history     []jobrepo.DeleteState
	failCommit  error
	transitions int
}

func newMemJobs() *memJobs {
	return &memJobs{jobs: map[string]jobrepo.DeleteJob{}}
}

func (m *memJobs) CreateDeleteJob(_ context.Context, job jobrepo.DeleteJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[job.JobID]; ok {
		return jobrepo.ErrDuplicateJob
	}
	m.jobs[job.JobID] = job
	m.history = append(m.history, job.State)
	return nil
}

func (m *memJobs) DeleteJob(_ context.Context, jobID string) (jobrepo.DeleteJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return jobrepo.DeleteJob{}, jobrepo.ErrJobNotFound
	}
	return job, nil
}

func (m *memJobs) TransitionDeleteJob(_ context.Context, jobID string, from, to jobrepo.DeleteState, patch jobrepo.DeletePatch, updatedAt int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return jobrepo.ErrJobNotFound
	}
	if job.State != from {
		return jobrepo.ErrStaleTransition
	}
	if to == jobrepo.DeleteCommitted && m.failCommit != nil {
		return m.failCommit
	}
	job.State = to
	job.UpdatedAt = updatedAt
	if patch.DelHeader != nil {
		job.DelHeader = *patch.DelHeader
	}
	if patch.DelMsgID != nil {
		job.DelMsgID = *patch.DelMsgID
	}
	if patch.ErrorCode != nil {
		job.ErrorCode = *patch.ErrorCode
	}
	if patch.IncrementAttempts {
		job.Attempts++
	}
	m.jobs[jobID] = job
	m.history = append(m.history, to)
	m.transitions++
	return nil
}

func (m *memJobs) only(t *testing.T) jobrepo.DeleteJob {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) != 1 {
		t.Fatalf("delete jobs = %d, want 1", len(m.jobs))
	}
	for _, job := range m.jobs {
		return job
	}
	return jobrepo.DeleteJob{}
}

func (m *memJobs) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

type sqlQueue struct {
	db       *sqlitedb.DB
	mu       sync.Mutex
	claims   [][]int64
	failDefs error
	lease    bool
	skip     map[int64]bool
}

var _ ingest.CleanupQueue = (*sqlQueue)(nil)

func (q *sqlQueue) newPass() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.skip = nil
}

func (q *sqlQueue) DueCleanupChannels(ctx context.Context, now int64, limit int) ([]int64, error) {
	var out []int64
	err := q.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT channel_id FROM pending_message_cleanup WHERE next_attempt_at <= ?1 ORDER BY channel_id LIMIT ?2`, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func (q *sqlQueue) ClaimCleanupBatch(ctx context.Context, channelID, now int64, limit int) ([]int64, error) {
	var out []int64
	err := q.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT msg_id FROM pending_message_cleanup WHERE channel_id = ?1 AND next_attempt_at <= ?2 ORDER BY msg_id`, channelID, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lease {
		kept := out[:0:0]
		for _, id := range out {
			if !q.skip[id] {
				kept = append(kept, id)
			}
		}
		out = kept
	}
	if len(out) > limit {
		out = out[:limit]
	}
	q.claims = append(q.claims, append([]int64(nil), out...))
	return out, err
}

func (q *sqlQueue) ConfirmCleanupDeleted(ctx context.Context, channelID int64, msgIDs []int64, recordedAt int64) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range msgIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM pending_message_cleanup WHERE channel_id = ?1 AND msg_id = ?2`, channelID, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, ?2, 'cleanup', ?3)`, channelID, id, recordedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

func (q *sqlQueue) DeferCleanup(ctx context.Context, channelID int64, msgIDs []int64, nextAttemptAt int64, lastError string) error {
	if q.failDefs != nil {
		return q.failDefs
	}
	q.mu.Lock()
	if q.skip == nil {
		q.skip = map[int64]bool{}
	}
	for _, id := range msgIDs {
		q.skip[id] = true
	}
	q.mu.Unlock()
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range msgIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE pending_message_cleanup SET attempts = attempts + 1, next_attempt_at = ?3, last_error = ?4 WHERE channel_id = ?1 AND msg_id = ?2`, channelID, id, nextAttemptAt, lastError); err != nil {
				return err
			}
		}
		return nil
	})
}

type opsLookup struct{ db *sqlitedb.DB }

func (o opsLookup) LookupOperation(ctx context.Context, channelID int64, opID string, opType wire.OpType) (int64, bool, error) {
	var msgID int64
	err := o.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT msg_id FROM projection_operations WHERE channel_id = ?1 AND op_id = ?2 AND op_type = ?3`, channelID, opID, string(opType)).Scan(&msgID)
	})
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return msgID, err == nil, err
}

type fixedPrincipal struct{ p Principal }

func (f fixedPrincipal) Principal(context.Context) (Principal, error) { return f.p, nil }

type recordedGates struct {
	mu       sync.Mutex
	acquired int
	released int
}

func (g *recordedGates) AcquireShared(context.Context, int64) (func(), error) {
	g.mu.Lock()
	g.acquired++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.released++
		g.mu.Unlock()
	}, nil
}

type openAuthority struct{ err error }

func (a openAuthority) RequireWriteAuthority(context.Context, int64) error { return a.err }

type memKeyLocks struct {
	mu    sync.Mutex
	held  map[string]bool
	taken int
}

func (k *memKeyLocks) Lock(_ context.Context, channelID int64, key string) (func(), error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.held == nil {
		k.held = map[string]bool{}
	}
	id := fmt.Sprintf("%d/%s", channelID, key)
	if k.held[id] {
		return nil, fmt.Errorf("key already locked: %s", id)
	}
	k.held[id] = true
	k.taken++
	return func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		delete(k.held, id)
	}, nil
}

func (k *memKeyLocks) anyHeld() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.held) > 0
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type spySender struct {
	inner *faketg.Client
	log   *eventLog
}

func (s spySender) SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error) {
	s.log.add("send")
	return s.inner.SendControlIdempotent(ctx, req)
}

type spyDeleter struct {
	inner *faketg.Client
	env   *env
	log   *eventLog
}

func (s spyDeleter) DeleteMessages(ctx context.Context, peer tgclient.InputPeer, ids []int64) error {
	state := "tombstone-missing"
	if s.env.count(`SELECT COUNT(*) FROM object_tombstones`) > 0 && s.env.count(`SELECT COUNT(*) FROM objects`) == 0 {
		state = "tombstone-projected"
	}
	s.log.add(fmt.Sprintf("delete:%d:%s", len(ids), state))
	return s.inner.DeleteMessages(ctx, peer, ids)
}

type shiftedReader struct {
	inner *faketg.Client
	mu    sync.Mutex
	shift int64
	fail  error
}

func (r *shiftedReader) GetMessages(ctx context.Context, peer tgclient.InputPeer, ids []int64) ([]tgclient.HistoryMessage, error) {
	r.mu.Lock()
	shift, fail := r.shift, r.fail
	r.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	msgs, err := r.inner.GetMessages(ctx, peer, ids)
	for i := range msgs {
		msgs[i].Date += shift
	}
	return msgs, err
}

type peerAdapter struct{ client *faketg.Client }

func (p peerAdapter) PeerFor(_ context.Context, channelID int64) (tgclient.InputPeer, error) {
	return p.client.PeerOf(channelID)
}

type env struct {
	t         *testing.T
	ctx       context.Context
	clock     *clock.Fake
	db        *sqlitedb.DB
	net       *faketg.Network
	client    *faketg.Client
	peer      tgclient.InputPeer
	projector *projlog.Projector
	jobs      *memJobs
	queue     *sqlQueue
	gates     *recordedGates
	locks     *memKeyLocks
	log       *eventLog
	svc       *Deleter
	reader    *shiftedReader
	drainer   *Drainer
	seq       int
}

type nopGeneration struct{}

func (nopGeneration) Advance(int64) {}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: nopGeneration{}})
	if err != nil {
		t.Fatalf("apply registry: %v", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: logger})
	if err != nil {
		t.Fatalf("projector: %v", err)
	}
	clk := clock.NewFake(time.Unix(1763000000, 0).UTC())
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("fake network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: 11, FirstName: "Owner", Username: "owner"})
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	peer, err := client.CreateChannel(ctx, bucketName, "")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	e := &env{t: t, ctx: ctx, clock: clk, db: db, net: network, client: client, peer: peer, projector: projector,
		jobs: newMemJobs(), queue: &sqlQueue{db: db, lease: true}, gates: &recordedGates{}, locks: &memKeyLocks{}, log: &eventLog{}}
	e.exec(`INSERT INTO channels (channel_id, kind) VALUES (?1, 'registry')`, registryChannel)
	create := wire.Op{
		Type: wire.OpBucketCreate, JobID: e.jobID(), Bucket: bucketName, BucketChannelID: peer.ChannelID,
		Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600, Timestamp: 1700000000,
	}
	text, err := wire.Format(create)
	if err != nil {
		t.Fatalf("format bucket create: %v", err)
	}
	e.project(projection.Observed{ChannelID: registryChannel, MsgID: 2, MsgDate: 1700000002, RawText: text})
	e.reader = &shiftedReader{inner: client}
	e.drainer = e.newDrainer(e.queue, spyDeleter{inner: client, env: e, log: e.log})
	e.svc = e.newService(e.drainer)
	return e
}

func (e *env) newDrainer(queue ingest.CleanupQueue, deleter MessageDeleter) *Drainer {
	e.t.Helper()
	d, err := NewDrainer(DrainerDependencies{
		Queue: queue, Peers: peerAdapter{client: e.client}, Messages: deleter, Clock: e.clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		e.t.Fatalf("new drainer: %v", err)
	}
	return d
}

func (e *env) deps(drainer ingest.CleanupDrainer) Dependencies {
	buckets, err := bucketrepo.New(e.db)
	if err != nil {
		e.t.Fatalf("bucket repo: %v", err)
	}
	objects, err := objectrepo.New(e.db)
	if err != nil {
		e.t.Fatalf("object repo: %v", err)
	}
	return Dependencies{
		Buckets: buckets, Gates: e.gates, Authority: openAuthority{}, KeyLocks: e.locks, Objects: objects,
		Operations: opsLookup{db: e.db}, Jobs: e.jobs, Sender: spySender{inner: e.client, log: e.log},
		Peers: peerAdapter{client: e.client}, Messages: e.reader, Projector: e.projector, Writer: e.db, Drainer: drainer,
		Principals:     fixedPrincipal{p: Principal{Origin: jobrepo.OriginS3, Scope: "key:AKIATEST", Writer: testAccessKey, RequestID: "req-1"}},
		StableRandomID: send.StableRandomID, Clock: e.clock, Random: strings.NewReader(strings.Repeat("\x07", 4096)),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ActorUserID: e.client.UserID(),
	}
}

func (e *env) newService(drainer ingest.CleanupDrainer) *Deleter {
	e.t.Helper()
	svc, err := New(e.deps(drainer))
	if err != nil {
		e.t.Fatalf("new service: %v", err)
	}
	return svc
}

func (e *env) jobID() string {
	e.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", e.seq)
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, query, args...)
		return err
	}); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(e.ctx, query, args...).Scan(&n)
	}); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) project(obs projection.Observed) {
	e.t.Helper()
	outcome, err := e.projector.ProjectFromOp(e.ctx, e.db, obs)
	if err != nil {
		e.t.Fatalf("project message %d: %v", obs.MsgID, err)
	}
	if outcome.Kind != projection.OutcomeApplied {
		e.t.Fatalf("project message %d: outcome %s %s", obs.MsgID, outcome.Kind, outcome.RejectCode)
	}
}

type seeded struct {
	BodyMsgID int64
	PutMsgID  int64
}

func (e *env) seedObject(key string) seeded {
	e.t.Helper()
	bodyText, err := wire.Format(wire.Op{Type: wire.OpBody, JobID: e.jobID(), StoredSize: 5})
	if err != nil {
		e.t.Fatalf("format body: %v", err)
	}
	bodyID, err := e.net.SeedDocument(e.peer.ChannelID, "body.bin", bodyText, []byte("hello"))
	if err != nil {
		e.t.Fatalf("seed body: %v", err)
	}
	putJob := e.jobID()
	putText, err := wire.Format(wire.Op{
		Type: wire.OpPut, JobID: putJob, Key: key, BodyMsgID: bodyID, StoredSize: 5, PlaintextSize: 5,
		ETag: "5d41402abc4b2a76b9719d911017c592", SHA256: wire.EmptySHA256, ContentType: "text/plain",
		Writer: testAccessKey, Timestamp: 1700000000,
	})
	if err != nil {
		e.t.Fatalf("format put: %v", err)
	}
	putID, err := e.client.SendControlIdempotent(e.ctx, tgclient.SendControlRequest{Peer: e.peer, Text: putText, RandomID: int64(e.seq)*2 + 1})
	if err != nil {
		e.t.Fatalf("send put: %v", err)
	}
	e.project(projection.Observed{ChannelID: e.peer.ChannelID, MsgID: bodyID, MsgDate: 1700000000, IsDocument: true, RawText: bodyText})
	e.project(projection.Observed{ChannelID: e.peer.ChannelID, MsgID: putID, MsgDate: 1700000001, RawText: putText})
	return seeded{BodyMsgID: bodyID, PutMsgID: putID}
}

func (e *env) liveObjects() int {
	return e.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, e.peer.ChannelID)
}

func (e *env) pending() int {
	return e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1`, e.peer.ChannelID)
}

func (e *env) deletedByCleanup() int {
	return e.count(`SELECT COUNT(*) FROM deleted_messages WHERE channel_id = ?1 AND source = 'cleanup'`, e.peer.ChannelID)
}

func (e *env) channelHas(msgID int64) bool {
	e.t.Helper()
	msgs, err := e.net.Messages(e.peer.ChannelID)
	if err != nil {
		e.t.Fatalf("channel messages: %v", err)
	}
	for _, msg := range msgs {
		if msg.MsgID == msgID {
			return true
		}
	}
	return false
}

func (e *env) queuePad(count int) []int64 {
	e.t.Helper()
	ids := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		id, err := e.net.SeedDocument(e.peer.ChannelID, "pad.bin", "", []byte("x"))
		if err != nil {
			e.t.Fatalf("seed pad: %v", err)
		}
		e.exec(`INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id) VALUES (?1, ?2, 'orphan_body', NULL, 0)`, e.peer.ChannelID, id)
		ids = append(ids, id)
	}
	return ids
}
