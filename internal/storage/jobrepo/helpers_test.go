package jobrepo

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const jobsTestChannel int64 = 1001

var jobsTestStart = time.Unix(1763000000, 0).UTC()

type jobsEnv struct {
	repo  *Repository
	db    *sqlitedb.DB
	clock *clock.Fake
}

func newJobsEnv(t *testing.T) jobsEnv {
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
	clk := clock.NewFake(jobsTestStart)
	repo, err := New(db, clk)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return jobsEnv{repo: repo, db: db, clock: clk}
}

func jobsTestID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-000000000000", n)
}

func sampleUploadJob(id string) UploadJob {
	return UploadJob{
		JobID:           id,
		Kind:            KindPut,
		Origin:          OriginS3,
		Scope:           "key:AKIAEXAMPLE",
		Writer:          "key.AKIAEXAMPLE",
		BucketChannelID: jobsTestChannel,
		BucketName:      "bucket",
		ObjectKey:       "photos/a.jpg",
		State:           UploadPrepared,
		SpoolPath:       "spool/" + id + ".plain",
		PlaintextSize:   10,
		StoredSize:      10,
		ETag:            strings.Repeat("a", 32),
		SHA256:          strings.Repeat("b", 64),
		ContentType:     "image/jpeg",
	}
}

func sampleEncryptedUploadJob(id string) UploadJob {
	job := sampleUploadJob(id)
	job.Encrypted = true
	job.EncryptionVersion = 1
	job.KeyID = strings.Repeat("c", 16)
	job.FileSalt = []byte(strings.Repeat("s", 16))
	job.NoncePrefix = []byte(strings.Repeat("n", 20))
	job.StoredSize = 10 + 64
	return job
}

func sampleDeleteJob(id string) DeleteJob {
	return DeleteJob{
		JobID:           id,
		Origin:          OriginS3,
		Scope:           "key:AKIAEXAMPLE",
		Writer:          "key.AKIAEXAMPLE",
		BucketChannelID: jobsTestChannel,
		BucketName:      "bucket",
		ObjectKey:       "photos/a.jpg",
		State:           DeletePrepared,
		TargetPutMsgID:  42,
	}
}

func (e jobsEnv) mustCreateUpload(t *testing.T, job UploadJob) {
	t.Helper()
	if err := e.repo.CreateUpload(context.Background(), job); err != nil {
		t.Fatalf("create upload %s: %v", job.JobID, err)
	}
}

func (e jobsEnv) mustCreateDelete(t *testing.T, job DeleteJob) {
	t.Helper()
	if err := e.repo.CreateDelete(context.Background(), job); err != nil {
		t.Fatalf("create delete %s: %v", job.JobID, err)
	}
}

func (e jobsEnv) rawExec(t *testing.T, statement string, args ...any) {
	t.Helper()
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), statement, args...)
		return err
	})
	if err != nil {
		t.Fatalf("raw exec %q: %v", statement, err)
	}
}

func strPtr(value string) *string { return &value }

func int64Ptr(value int64) *int64 { return &value }
