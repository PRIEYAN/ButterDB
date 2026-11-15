package multipartstage

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const testChannel int64 = 4242

var testStart = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

type readableFS struct {
	*fsx.Rooted
	mu    sync.Mutex
	calls []string
}

func (f *readableFS) Open(name string) (io.ReadCloser, error) {
	f.record("open")
	return os.Open(name)
}

func (f *readableFS) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *readableFS) MkdirAll(path string, perm fs.FileMode) error {
	f.record("mkdir")
	return f.Rooted.MkdirAll(path, perm)
}

func (f *readableFS) CreateTemp(dir, pattern string) (fsx.File, error) {
	f.record("createtemp")
	return f.Rooted.CreateTemp(dir, pattern)
}

func (f *readableFS) Rename(oldpath, newpath string) error {
	f.record("rename")
	return f.Rooted.Rename(oldpath, newpath)
}

func (f *readableFS) Remove(name string) error {
	f.record("remove")
	return f.Rooted.Remove(name)
}

func (f *readableFS) Stat(name string) (fs.FileInfo, error) {
	f.record("stat")
	return f.Rooted.Stat(name)
}

func (f *readableFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f.record("readdir")
	return f.Rooted.ReadDir(name)
}

func (f *readableFS) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type sequenceRandom struct {
	mu   sync.Mutex
	next byte
}

func (r *sequenceRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

type guardedIndex struct {
	Index
	active atomic.Int32
}

func (g *guardedIndex) enter() func() {
	g.active.Add(1)
	return func() { g.active.Add(-1) }
}

func (g *guardedIndex) CreateUpload(ctx context.Context, u jobrepo.MultipartUpload) error {
	defer g.enter()()
	return g.Index.CreateUpload(ctx, u)
}

func (g *guardedIndex) GetUpload(ctx context.Context, id string) (jobrepo.MultipartUpload, error) {
	defer g.enter()()
	return g.Index.GetUpload(ctx, id)
}

func (g *guardedIndex) PutPart(ctx context.Context, p jobrepo.MultipartPart) error {
	defer g.enter()()
	return g.Index.PutPart(ctx, p)
}

func (g *guardedIndex) ListParts(ctx context.Context, id string, marker int, max int) ([]jobrepo.MultipartPart, bool, error) {
	defer g.enter()()
	return g.Index.ListParts(ctx, id, marker, max)
}

func (g *guardedIndex) ListUploads(ctx context.Context, channel int64, q UploadQuery) ([]jobrepo.MultipartUpload, bool, error) {
	defer g.enter()()
	return g.Index.ListUploads(ctx, channel, q)
}

func (g *guardedIndex) Transition(ctx context.Context, id string, from, to string, patch jobrepo.MultipartPatch) error {
	defer g.enter()()
	return g.Index.Transition(ctx, id, from, to, patch)
}

func (g *guardedIndex) Delete(ctx context.Context, id string) error {
	defer g.enter()()
	return g.Index.Delete(ctx, id)
}

func (g *guardedIndex) IdleBefore(ctx context.Context, t time.Time, limit int) ([]string, error) {
	defer g.enter()()
	return g.Index.IdleBefore(ctx, t, limit)
}

type putCall struct {
	req      object.PutRequest
	body     []byte
	uploadID string
	jobID    string
	etag     string
	busy     bool
}

type fakePutter struct {
	index   *guardedIndex
	mu      sync.Mutex
	calls   []putCall
	err     error
	msgID   int64
	started chan struct{}
	release chan struct{}
}

func (p *fakePutter) PutMultipart(_ context.Context, req object.PutRequest, body io.Reader, uploadID, jobID, etag string) (object.ObjectMeta, error) {
	busy := p.index != nil && p.index.active.Load() != 0
	if p.started != nil {
		p.started <- struct{}{}
		<-p.release
	}
	data, readErr := io.ReadAll(body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, putCall{req: req, body: data, uploadID: uploadID, jobID: jobID, etag: etag, busy: busy})
	if readErr != nil {
		return object.ObjectMeta{}, readErr
	}
	if p.err != nil {
		return object.ObjectMeta{}, p.err
	}
	msgID := p.msgID
	if msgID == 0 {
		msgID = 9001
	}
	return object.ObjectMeta{ChannelID: testChannel, Key: string(req.Key), PutMsgID: msgID, JobID: jobID, PlaintextSize: int64(len(data))}, nil
}

func (p *fakePutter) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakePutter) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type harness struct {
	stage  *Stage
	index  *guardedIndex
	real   *jobrepo.MultipartIndex
	db     *sqlitedb.DB
	putter *fakePutter
	clock  *clock.Fake
	fs     *readableFS
	dir    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Control, filepath.Join(t.TempDir(), "control.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Control); err != nil {
		t.Fatalf("apply control schema: %v", err)
	}
	real, err := jobrepo.NewMultipartIndex(db)
	if err != nil {
		t.Fatalf("new multipart index: %v", err)
	}
	root, err := fsx.NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	files := &readableFS{Rooted: root}
	index := &guardedIndex{Index: real}
	putter := &fakePutter{index: index}
	fake := clock.NewFake(testStart)
	dir := filepath.Join(root.Dir(), "multipart")
	stage, err := New(Dependencies{Index: index, Putter: putter, Dir: dir, Clock: fake, Random: &sequenceRandom{}, FS: files})
	if err != nil {
		t.Fatalf("new stage: %v", err)
	}
	return &harness{stage: stage, index: index, real: real, db: db, putter: putter, clock: fake, fs: files, dir: dir}
}

func (h *harness) create(t *testing.T, key string, encrypted bool) Upload {
	t.Helper()
	upload, err := h.stage.Create(context.Background(), CreateRequest{
		Bucket:          "photos",
		BucketChannelID: testChannel,
		Key:             object.Key(key),
		ContentType:     "video/mp4",
		UserMeta:        map[string]string{"owner": "alice"},
		SystemMeta:      map[string]string{"cache-control": "no-cache"},
		Encrypted:       encrypted,
		AccessKeyID:     "AKIATEST",
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	return upload
}

func (h *harness) putPart(t *testing.T, uploadID string, number int, data []byte) Part {
	t.Helper()
	part, err := h.stage.PutPart(context.Background(), uploadID, number, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("put part %d: %v", number, err)
	}
	return part
}

func (h *harness) partFile(uploadID string, number int) string {
	return h.stage.partPath(uploadID, number)
}

func filled(size int, value byte) []byte {
	return bytes.Repeat([]byte{value}, size)
}
