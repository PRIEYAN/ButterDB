package sweeper

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

	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var (
	_ ingest.CleanupQueue = (*objectrepo.CleanupQueue)(nil)
	_ JobStore            = (*jobrepo.Repository)(nil)
	_ DueCleanup          = (*objectrepo.CleanupQueue)(nil)
	_ OrphanBodies        = (*objectrepo.CleanupQueue)(nil)
)

var testStart = time.Unix(1763000000, 0).UTC()

type recordingResumer struct {
	mu     sync.Mutex
	calls  []string
	handle func(ctx context.Context, jobID string) error
}

func (r *recordingResumer) Resume(ctx context.Context, jobID string) error {
	r.mu.Lock()
	r.calls = append(r.calls, jobID)
	handle := r.handle
	r.mu.Unlock()
	if handle == nil {
		return nil
	}
	return handle(ctx, jobID)
}

func (r *recordingResumer) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type recordingDrainer struct {
	mu     sync.Mutex
	calls  []int64
	handle func(ctx context.Context, channelID int64) error
}

func (d *recordingDrainer) DrainChannel(ctx context.Context, channelID int64) error {
	d.mu.Lock()
	d.calls = append(d.calls, channelID)
	handle := d.handle
	d.mu.Unlock()
	if handle == nil {
		return nil
	}
	return handle(ctx, channelID)
}

func (d *recordingDrainer) called() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int64(nil), d.calls...)
}

type gateSleeper struct {
	slept   chan time.Duration
	release chan struct{}
}

func newGateSleeper() *gateSleeper {
	return &gateSleeper{slept: make(chan time.Duration, 64), release: make(chan struct{}, 64)}
}

func (g *gateSleeper) Sleep(ctx context.Context, d time.Duration) error {
	g.slept <- d
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type env struct {
	jobs     *jobrepo.Repository
	queue    *objectrepo.CleanupQueue
	control  *sqlitedb.DB
	proj     *sqlitedb.DB
	clock    *clock.Fake
	uploads  *recordingResumer
	deletes  *recordingResumer
	drainer  *recordingDrainer
	sleeper  *gateSleeper
	sweeper  *Sweeper
	settings Config
}

func openDB(t *testing.T, name sqlitedb.Name, set schema.Set, file string) *sqlitedb.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), name, filepath.Join(t.TempDir(), file), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, set); err != nil {
		t.Fatalf("apply %s schema: %v", file, err)
	}
	return db
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		control: openDB(t, sqlitedb.Control, schema.Control, "control.db"),
		proj:    openDB(t, sqlitedb.Projection, schema.Projection, "projection.db"),
		clock:   clock.NewFake(testStart),
		uploads: &recordingResumer{},
		deletes: &recordingResumer{},
		drainer: &recordingDrainer{},
		sleeper: newGateSleeper(),
	}
	var err error
	if e.jobs, err = jobrepo.New(e.control, e.clock); err != nil {
		t.Fatalf("jobrepo: %v", err)
	}
	if e.queue, err = objectrepo.NewCleanupQueue(e.proj); err != nil {
		t.Fatalf("cleanup queue: %v", err)
	}
	e.build(t)
	return e
}

func (e *env) dependencies() Dependencies {
	return Dependencies{
		Jobs: e.jobs, Uploads: e.uploads, Deletes: e.deletes, Due: e.queue, Drainer: e.drainer, Orphans: e.queue,
		Sleeper: e.sleeper, Clock: e.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Settings: e.settings,
	}
}

func (e *env) build(t *testing.T) {
	t.Helper()
	s, err := New(e.dependencies())
	if err != nil {
		t.Fatalf("new sweeper: %v", err)
	}
	e.sweeper = s
}

const testChannel int64 = 1001

func testJobID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-000000000000", n)
}

func uploadJob(id string) jobrepo.UploadJob {
	return jobrepo.UploadJob{
		JobID: id, Kind: jobrepo.KindPut, Origin: jobrepo.OriginS3, Scope: "key:AKIAEXAMPLE", Writer: "key.AKIAEXAMPLE",
		BucketChannelID: testChannel, BucketName: "bucket", ObjectKey: "a.txt", State: jobrepo.UploadPrepared,
		SpoolPath: "spool/" + id + ".plain", PlaintextSize: 3, StoredSize: 3,
		ETag: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), ContentType: "text/plain",
	}
}

func deleteJob(id string) jobrepo.DeleteJob {
	return jobrepo.DeleteJob{
		JobID: id, Origin: jobrepo.OriginS3, Scope: "key:AKIAEXAMPLE", Writer: "key.AKIAEXAMPLE",
		BucketChannelID: testChannel, BucketName: "bucket", ObjectKey: "a.txt", State: jobrepo.DeletePrepared, TargetPutMsgID: 9,
	}
}

func (e *env) exec(t *testing.T, db *sqlitedb.DB, statement string, args ...any) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), statement, args...)
		return err
	})
	if err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

func (e *env) count(t *testing.T, db *sqlitedb.DB, query string, args ...any) int {
	t.Helper()
	var n int
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func strPtr(s string) *string { return &s }
