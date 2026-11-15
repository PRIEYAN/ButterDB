package put

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection/apply"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	testChannelID   = int64(2000000001)
	testUserID      = int64(777000123)
	testAccessKeyID = "BDBK7Q2M9X4T1W8Z3R6Y"
	testBucketName  = "photos"
	testFingerprint = "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"
	testKeyID       = "437ad4b129840534"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stableRandomID(jobID, purpose string) (int64, error) {
	digest := sha256.Sum256([]byte(jobID + "\x00" + purpose))
	return int64(binary.LittleEndian.Uint64(digest[:8])) | 1, nil
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

type fakeSpool struct {
	dir     string
	mu      sync.Mutex
	writes  int
	removed []string
}

func (f *fakeSpool) Write(_ context.Context, jobID string, body io.Reader, declared int64, maxStored int64) (spool.File, error) {
	if declared > maxStored {
		return spool.File{}, fmt.Errorf("fakespool: declared %d over cap %d", declared, maxStored)
	}
	data, err := io.ReadAll(io.LimitReader(body, declared+1))
	if err != nil {
		return spool.File{}, err
	}
	if int64(len(data)) != declared {
		return spool.File{}, fmt.Errorf("fakespool: body is %d bytes, declared %d", len(data), declared)
	}
	path := filepath.Join(f.dir, jobID+".plain")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return spool.File{}, err
	}
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return spool.File{Path: path, Size: declared, MD5: md5.Sum(data), SHA256: sha256.Sum256(data)}, nil
}

func (f *fakeSpool) Open(file spool.File) (io.ReadSeekCloser, error) {
	return os.Open(file.Path)
}

func (f *fakeSpool) Remove(jobID string) error {
	f.mu.Lock()
	f.removed = append(f.removed, jobID)
	f.mu.Unlock()
	err := os.Remove(filepath.Join(f.dir, jobID+".plain"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (f *fakeSpool) exists(jobID string) bool {
	_, err := os.Stat(filepath.Join(f.dir, jobID+".plain"))
	return err == nil
}

type slowDown struct{ wait time.Duration }

func (e *slowDown) Error() string { return "slow down" }
func (e *slowDown) Unwrap() error { return errs.ErrSlowDown }

type fakeAdmission struct {
	mu            sync.Mutex
	acquired      []string
	released      int
	sends         []flow.SendClass
	floods        []time.Duration
	sendErr       error
	floodBudget   time.Duration
	refuseAcquire error
}

func (a *fakeAdmission) AcquirePut(_ context.Context, _ admission.Budget, scope string) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refuseAcquire != nil {
		return nil, a.refuseAcquire
	}
	a.acquired = append(a.acquired, scope)
	return func() {
		a.mu.Lock()
		a.released++
		a.mu.Unlock()
	}, nil
}

func (a *fakeAdmission) WaitSend(_ context.Context, _ admission.Budget, class flow.SendClass) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sends = append(a.sends, class)
	return a.sendErr
}

func (a *fakeAdmission) WaitFlood(_ context.Context, _ admission.Budget, wait time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.floods = append(a.floods, wait)
	if wait > a.floodBudget {
		return &slowDown{wait: wait}
	}
	return nil
}

var allowedTransitions = map[jobrepo.UploadState][]jobrepo.UploadState{
	jobrepo.UploadPrepared:      {jobrepo.UploadBodySending, jobrepo.UploadPutAttempted, jobrepo.UploadAborted},
	jobrepo.UploadBodySending:   {jobrepo.UploadBodySent, jobrepo.UploadUncertainBody, jobrepo.UploadAborted},
	jobrepo.UploadUncertainBody: {jobrepo.UploadBodySent, jobrepo.UploadAborted},
	jobrepo.UploadBodySent:      {jobrepo.UploadPutAttempted, jobrepo.UploadAborted},
	jobrepo.UploadPutAttempted:  {jobrepo.UploadCommitted, jobrepo.UploadUncertainPut},
	jobrepo.UploadUncertainPut:  {jobrepo.UploadCommitted, jobrepo.UploadPutAttempted, jobrepo.UploadAborted},
}

type memJobs struct {
	mu          sync.Mutex
	jobs        map[string]jobrepo.UploadJob
	idempotency map[string]jobrepo.IdempotencyRecord
	history     []jobrepo.UploadState
	failNext    map[jobrepo.UploadState]error
}

func newMemJobs() *memJobs {
	return &memJobs{
		jobs:        map[string]jobrepo.UploadJob{},
		idempotency: map[string]jobrepo.IdempotencyRecord{},
		failNext:    map[jobrepo.UploadState]error{},
	}
}

func rowInvariant(job jobrepo.UploadJob) error {
	switch {
	case job.Encrypted && (len(job.FileSalt) != 16 || len(job.NoncePrefix) != 20 || len(job.KeyID) != 16):
		return fmt.Errorf("%w: encrypted row without salt, nonce prefix and key id", jobrepo.ErrCorruptRow)
	case !job.Encrypted && (job.FileSalt != nil || job.NoncePrefix != nil || job.PlaintextSize != job.StoredSize):
		return fmt.Errorf("%w: plain row with encryption fields", jobrepo.ErrCorruptRow)
	case (job.State == jobrepo.UploadPutAttempted || job.State == jobrepo.UploadUncertainPut || job.State == jobrepo.UploadCommitted) && job.PutHeader == "":
		return fmt.Errorf("%w: put state without header", jobrepo.ErrCorruptRow)
	case job.State == jobrepo.UploadCommitted && job.PutMsgID <= 0:
		return fmt.Errorf("%w: committed without put message", jobrepo.ErrCorruptRow)
	case job.State == jobrepo.UploadBodySent && job.BodyMsgID <= 0:
		return fmt.Errorf("%w: body sent without message", jobrepo.ErrCorruptRow)
	}
	return nil
}

func (m *memJobs) CreateJob(_ context.Context, job jobrepo.UploadJob, idem *jobrepo.IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[job.JobID]; exists {
		return jobrepo.ErrDuplicateJob
	}
	if err := rowInvariant(job); err != nil {
		return err
	}
	m.jobs[job.JobID] = job
	if idem != nil {
		m.idempotency[idem.Scope+"\x00"+idem.IdempotencyKey] = *idem
	}
	return nil
}

func (m *memJobs) GetJob(_ context.Context, jobID string) (jobrepo.UploadJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return jobrepo.UploadJob{}, jobrepo.ErrJobNotFound
	}
	return job, nil
}

func (m *memJobs) TransitionJob(_ context.Context, jobID string, from, to jobrepo.UploadState, patch jobrepo.UploadPatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return jobrepo.ErrJobNotFound
	}
	if err := m.failNext[to]; err != nil {
		delete(m.failNext, to)
		return err
	}
	allowed := false
	for _, next := range allowedTransitions[from] {
		allowed = allowed || next == to
	}
	if !allowed {
		return fmt.Errorf("%w: %s to %s", jobrepo.ErrInvalidTransition, from, to)
	}
	if job.State != from {
		return fmt.Errorf("%w: row is %s, caller expected %s", jobrepo.ErrStaleTransition, job.State, from)
	}
	job.State = to
	if patch.SealedPath != nil {
		job.SealedPath = *patch.SealedPath
	}
	if patch.BodyMsgID != nil {
		job.BodyMsgID = *patch.BodyMsgID
	}
	if patch.PutHeader != nil {
		job.PutHeader = *patch.PutHeader
	}
	if patch.PutMsgID != nil {
		job.PutMsgID = *patch.PutMsgID
	}
	if patch.ErrorCode != nil {
		job.ErrorCode = *patch.ErrorCode
	}
	if patch.IncrementAttempts {
		job.Attempts++
	}
	if err := rowInvariant(job); err != nil {
		return err
	}
	m.jobs[jobID] = job
	m.history = append(m.history, to)
	return nil
}

func (m *memJobs) LookupIdempotency(_ context.Context, scope, key string) (jobrepo.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.idempotency[scope+"\x00"+key]
	return record, ok, nil
}

func (m *memJobs) only() jobrepo.UploadJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) != 1 {
		panic(fmt.Sprintf("want exactly one job, have %d", len(m.jobs)))
	}
	for _, job := range m.jobs {
		return job
	}
	return jobrepo.UploadJob{}
}

func (m *memJobs) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

type fakeBuckets struct{ info bucket.Info }

func (b *fakeBuckets) Lookup(_ context.Context, name bucket.BucketName) (bucket.Info, bool, error) {
	if name != b.info.Name {
		return bucket.Info{}, false, nil
	}
	return b.info, true, nil
}

type fakeAuthority struct {
	mu        sync.Mutex
	ensured   int
	denyWrite error
}

func (a *fakeAuthority) RequireWriteAuthority(context.Context, int64) error { return a.denyWrite }

func (a *fakeAuthority) EnsureKey(context.Context, int64) error {
	a.mu.Lock()
	a.ensured++
	a.mu.Unlock()
	return nil
}

type countingGate struct {
	mu       sync.Mutex
	acquired int
	released int
}

func (g *countingGate) AcquireShared(context.Context, int64) (func(), error) {
	g.mu.Lock()
	g.acquired++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.released++
		g.mu.Unlock()
	}, nil
}

type fakeKeys struct{}

func (fakeKeys) ActiveKey(_ context.Context, _ int64, fingerprint string) (EncryptionKey, bool, error) {
	if fingerprint != testFingerprint {
		return EncryptionKey{}, false, nil
	}
	return EncryptionKey{KeyID: testKeyID, WrappedDEK: bytes.Repeat([]byte{1}, 72), KeyCheck: bytes.Repeat([]byte{2}, 61)}, true, nil
}

func (fakeKeys) Key(_ context.Context, _ int64, keyID string) (EncryptionKey, bool, error) {
	if keyID != testKeyID {
		return EncryptionKey{}, false, nil
	}
	return EncryptionKey{KeyID: testKeyID, WrappedDEK: bytes.Repeat([]byte{1}, 72), KeyCheck: bytes.Repeat([]byte{2}, 61)}, true, nil
}

type trackedDEK struct {
	raw     []byte
	cleared bool
}

func (d *trackedDEK) Bytes() []byte { return d.raw }

func (d *trackedDEK) Clear() {
	clear(d.raw)
	d.cleared = true
}

type fakeUnwrap struct {
	mu   sync.Mutex
	deks []*trackedDEK
}

var testDEK = bytes.Repeat([]byte{0x42}, 32)

func (u *fakeUnwrap) UnwrapDEK(_ []byte, _ []byte, _ int64, _ string) (DEK, error) {
	dek := &trackedDEK{raw: append([]byte(nil), testDEK...)}
	u.mu.Lock()
	u.deks = append(u.deks, dek)
	u.mu.Unlock()
	return dek, nil
}

type fakeCleanup struct {
	mu      sync.Mutex
	drained []int64
	err     error
}

func (c *fakeCleanup) DrainChannel(_ context.Context, channelID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drained = append(c.drained, channelID)
	return c.err
}

type fileCall struct {
	randomID int64
	caption  string
	size     int64
}

type recordingSender struct {
	inner      *faketg.Client
	mu         sync.Mutex
	files      []fileCall
	controls   []int64
	controlCtx []context.Context
	fileHook   func(call int, req tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool)
	ctlHook    func(call int, req tgclient.SendControlRequest) (int64, error, bool)
	resolves   int
}

func (r *recordingSender) SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error) {
	r.mu.Lock()
	r.files = append(r.files, fileCall{randomID: req.RandomID, caption: req.Caption, size: req.Size})
	call := len(r.files)
	hook := r.fileHook
	r.mu.Unlock()
	if hook != nil {
		if result, err, handled := hook(call, req); handled {
			return result, err
		}
	}
	return r.inner.SendFileIdempotent(ctx, req)
}

func (r *recordingSender) SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error) {
	r.mu.Lock()
	r.controls = append(r.controls, req.RandomID)
	r.controlCtx = append(r.controlCtx, ctx)
	call := len(r.controls)
	hook := r.ctlHook
	r.mu.Unlock()
	if hook != nil {
		if msgID, err, handled := hook(call, req); handled {
			return msgID, err
		}
	}
	return r.inner.SendControlIdempotent(ctx, req)
}

func (r *recordingSender) ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	r.mu.Lock()
	r.resolves++
	r.mu.Unlock()
	return r.inner.ResolveChannel(ctx, channelID)
}

func (r *recordingSender) GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error) {
	return r.inner.GetMessages(ctx, peer, msgIDs)
}

func (r *recordingSender) DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error {
	return r.inner.DeleteMessages(ctx, peer, msgIDs)
}

type world struct {
	t         *testing.T
	ctx       context.Context
	db        *sqlitedb.DB
	clock     *clock.Fake
	network   *faketg.Network
	telegram  *faketg.Client
	sender    *recordingSender
	spool     *fakeSpool
	admission *fakeAdmission
	jobs      *memJobs
	authority *fakeAuthority
	gate      *countingGate
	unwrap    *fakeUnwrap
	cleanup   *fakeCleanup
	objects   *objectrepo.Repository
	deps      Dependencies
	svc       *Putter
}

func newWorld(t *testing.T, defaultEncryption string) *world {
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
	if _, err := network.SeedChannel(testChannelID, "photos", "", telegram); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	seedBucket(t, db, defaultEncryption)
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: noGeneration{}})
	if err != nil {
		t.Fatalf("new apply registry: %v", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	objects, err := objectrepo.New(db)
	if err != nil {
		t.Fatalf("new object repo: %v", err)
	}
	w := &world{
		t: t, ctx: ctx, db: db, clock: clk, network: network, telegram: telegram,
		sender:    &recordingSender{inner: telegram},
		spool:     &fakeSpool{dir: t.TempDir()},
		admission: &fakeAdmission{floodBudget: time.Minute},
		jobs:      newMemJobs(),
		authority: &fakeAuthority{},
		gate:      &countingGate{},
		unwrap:    &fakeUnwrap{},
		cleanup:   &fakeCleanup{},
		objects:   objects,
	}
	w.deps = Dependencies{
		Spool: w.spool, Admission: w.admission, Jobs: w.jobs,
		Buckets:   &fakeBuckets{info: bucket.Info{Name: testBucketName, ChannelID: testChannelID, DefaultEncryption: defaultEncryption}},
		Authority: w.authority, Gates: w.gate, Objects: objects, Keys: fakeKeys{}, Unwrap: w.unwrap,
		Peers: w.sender, Bodies: w.sender, Controls: w.sender, Deleter: w.sender, Messages: w.sender,
		Projector: projector, Writer: db, Cleanup: w.cleanup, StableRandomID: stableRandomID,
		KeyLocks: NewKeyLocks(), Clock: clk, Random: &counterReader{}, Logger: discardLogger(),
		Config: Config{RequestBudget: 30 * time.Second, KEKFingerprint: testFingerprint, ActorUserID: testUserID},
	}
	w.svc, err = New(w.deps)
	if err != nil {
		t.Fatalf("new put service: %v", err)
	}
	return w
}

func seedBucket(t *testing.T, db *sqlitedb.DB, defaultEncryption string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO channels (channel_id, kind, access_hash, title, initial_sync_done) VALUES (?1, 'bucket', 1, 'photos', 1)`, testChannelID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id)
VALUES (?1, ?2, 'private', ?3, 0, 1, '00000000-0000-4000-8000-000000000001', 1763000000, 1)`, testChannelID, testBucketName, defaultEncryption)
		return err
	})
	if err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
}

func (w *world) putRequest(key string, size int) object.PutRequest {
	return object.PutRequest{Bucket: testBucketName, Key: object.Key(key), ContentLength: int64(size), AccessKeyID: testAccessKeyID}
}

func (w *world) sentFiles() []faketg.SentFile { return w.network.SentFiles(testChannelID) }

func (w *world) sentControls() []faketg.SentControl { return w.network.SentControls(testChannelID) }

func (w *world) deleted() [][]int64 { return w.network.DeletedBatches(testChannelID) }

func (w *world) liveObject(key string) (object.ObjectMeta, bool) {
	w.t.Helper()
	meta, err := w.objects.Get(w.ctx, testChannelID, key)
	if errors.Is(err, errs.ErrNoSuchKey) {
		return object.ObjectMeta{}, false
	}
	if err != nil {
		w.t.Fatalf("load object %q: %v", key, err)
	}
	return meta, true
}

func (w *world) countRows(query string, args ...any) int {
	w.t.Helper()
	var n int
	err := w.db.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(query, args...).Scan(&n)
	})
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (w *world) readBody(msgID int64) []byte {
	w.t.Helper()
	peer, err := w.telegram.ResolveChannel(w.ctx, testChannelID)
	if err != nil {
		w.t.Fatalf("resolve channel: %v", err)
	}
	ref, err := w.telegram.ResolveDocument(w.ctx, peer, msgID)
	if err != nil {
		w.t.Fatalf("resolve document %d: %v", msgID, err)
	}
	var out []byte
	for offset := int64(0); offset < ref.Size; {
		chunk := make([]byte, min(ref.Size-offset, 4096*16))
		n, err := w.telegram.ReadDocumentRange(w.ctx, ref, offset, chunk)
		if err != nil {
			w.t.Fatalf("read document: %v", err)
		}
		out = append(out, chunk[:n]...)
		offset += int64(n)
	}
	return out
}

func bytesOf(size int, fill byte) []byte { return bytes.Repeat([]byte{fill}, size) }

func (w *world) prepare(key string, body []byte, mutate func(*object.PutRequest)) (*resolved, jobrepo.UploadJob) {
	w.t.Helper()
	req := w.putRequest(key, len(body))
	if mutate != nil {
		mutate(&req)
	}
	p, err := planRequest(req)
	if err != nil {
		w.t.Fatalf("plan request: %v", err)
	}
	res, err := p.resolve(bucket.Info{Name: testBucketName, ChannelID: testChannelID, DefaultEncryption: w.deps.Buckets.(*fakeBuckets).info.DefaultEncryption})
	if err != nil {
		w.t.Fatalf("resolve: %v", err)
	}
	jobID, err := newJobID(w.deps.Random)
	if err != nil {
		w.t.Fatalf("job id: %v", err)
	}
	file, err := w.spool.Write(w.ctx, jobID, bytes.NewReader(body), int64(len(body)), maxPlaintextFor(res.encrypted))
	if err != nil {
		w.t.Fatalf("spool: %v", err)
	}
	job, err := w.svc.newJob(res, jobID, file, nil)
	if err != nil {
		w.t.Fatalf("new job: %v", err)
	}
	if err := w.jobs.CreateJob(w.ctx, job, nil); err != nil {
		w.t.Fatalf("create job: %v", err)
	}
	return &res, job
}

func (w *world) budget() admission.Budget {
	return admission.Budget{Deadline: w.clock.Now().Add(w.deps.Config.RequestBudget)}
}
