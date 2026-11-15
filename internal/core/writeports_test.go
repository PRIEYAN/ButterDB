package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/remove"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const testJobID = "0d3f6a52-91c4-4b8e-8a57-3c1e2f4d5a60"

func TestPrincipalForCaller(t *testing.T) {
	tests := []struct {
		name    string
		caller  string
		want    remove.Principal
		wantErr error
	}{
		{name: "access key", caller: "BDBK1", want: remove.Principal{Origin: jobrepo.OriginS3, Scope: "key:BDBK1", Writer: "key.BDBK1", RequestID: "r1"}},
		{name: "device", caller: "device:abc", want: remove.Principal{Origin: jobrepo.OriginAdmin, Scope: "device:abc", Writer: "dev.abc", RequestID: "r1"}},
		{name: "empty", caller: "", wantErr: errs.ErrAccessDenied},
		{name: "device prefix only", caller: "device:", wantErr: errs.ErrAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrincipalForCaller(tt.caller, "r1")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("principal = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPrincipalContextReadsWhatTheCallerAttached(t *testing.T) {
	if _, err := (principalContext{}).Principal(context.Background()); !errors.Is(err, errs.ErrAccessDenied) {
		t.Fatalf("bare context = %v, want ErrAccessDenied", err)
	}
	want := remove.Principal{Origin: jobrepo.OriginS3, Scope: "key:BDBK1", Writer: "key.BDBK1", RequestID: "r1"}
	got, err := (principalContext{}).Principal(WithPrincipal(context.Background(), want))
	if err != nil || got != want {
		t.Fatalf("principal = %+v, %v; want %+v", got, err, want)
	}
}

func openJobs(t *testing.T, w *registryWorld) *jobrepo.Repository {
	t.Helper()
	repo, err := jobrepo.New(w.controlDB, w.clk)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func sampleUpload(channelID int64) jobrepo.UploadJob {
	return jobrepo.UploadJob{
		JobID: testJobID, Kind: jobrepo.KindPut, Origin: jobrepo.OriginS3, Scope: "key:BDBK1", Writer: "key.BDBK1",
		BucketChannelID: channelID, BucketName: "photos", ObjectKey: "a.txt", State: jobrepo.UploadPrepared,
		SpoolPath: "spool/" + testJobID + ".plain", ETag: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64),
		ContentType: "text/plain",
	}
}

func TestActiveJobsCoversUploadAndDeleteJobs(t *testing.T) {
	ctx := context.Background()
	w := newRegistryWorld(t)
	jobs := openJobs(t, w)
	active := &activeJobs{db: w.controlDB}
	const channel = int64(2000000001)

	if got, err := active.HasActiveJobs(ctx, channel); err != nil || got {
		t.Fatalf("empty store = %v, %v; want false", got, err)
	}
	if err := jobs.CreateUpload(ctx, sampleUpload(channel)); err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if got, err := active.HasActiveJobs(ctx, channel); err != nil || !got {
		t.Fatalf("prepared upload = %v, %v; want true", got, err)
	}
	if got, err := active.HasActiveJobs(ctx, channel+1); err != nil || got {
		t.Fatalf("other bucket = %v, %v; want false", got, err)
	}
	code := "test"
	if err := jobs.TransitionUpload(ctx, testJobID, jobrepo.UploadPrepared, jobrepo.UploadAborted, jobrepo.UploadPatch{ErrorCode: &code}); err != nil {
		t.Fatalf("abort upload: %v", err)
	}
	if got, err := active.HasActiveJobs(ctx, channel); err != nil || got {
		t.Fatalf("aborted upload = %v, %v; want false", got, err)
	}
	del := jobrepo.DeleteJob{
		JobID: "1d3f6a52-91c4-4b8e-8a57-3c1e2f4d5a61", Origin: jobrepo.OriginS3, Scope: "key:BDBK1", Writer: "key.BDBK1",
		BucketChannelID: channel, BucketName: "photos", ObjectKey: "a.txt", State: jobrepo.DeletePrepared, TargetPutMsgID: 9,
	}
	if err := jobs.CreateDelete(ctx, del); err != nil {
		t.Fatalf("create delete: %v", err)
	}
	if got, err := active.HasActiveJobs(ctx, channel); err != nil || !got {
		t.Fatalf("prepared delete = %v, %v; want true", got, err)
	}
}

func TestJobsUnavailableFailsClosed(t *testing.T) {
	got, err := activeJobsFor(nil).HasActiveJobs(context.Background(), 1)
	if got || !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("HasActiveJobs = %v, %v; want false and ErrMissingDependency", got, err)
	}
}

func TestUploadJobsAdapter(t *testing.T) {
	ctx := context.Background()
	w := newRegistryWorld(t)
	jobs := &uploadJobs{repo: openJobs(t, w)}

	if _, found, err := jobs.LookupIdempotency(ctx, "key:BDBK1", "none"); err != nil || found {
		t.Fatalf("missing idempotency record = %v, %v; want found=false", found, err)
	}
	job := sampleUpload(2000000001)
	record := jobrepo.IdempotencyRecord{Scope: job.Scope, IdempotencyKey: "idem-1", BucketChannelID: job.BucketChannelID, ObjectKey: job.ObjectKey, BodySHA256: job.SHA256, JobID: job.JobID}
	if err := jobs.CreateJob(ctx, job, &record); err != nil {
		t.Fatalf("CreateJob with idempotency: %v", err)
	}
	got, found, err := jobs.LookupIdempotency(ctx, job.Scope, "idem-1")
	if err != nil || !found || got.JobID != job.JobID {
		t.Fatalf("idempotency record = %+v, %v, %v", got, found, err)
	}
	if err := jobs.CreateJob(ctx, job, nil); !errors.Is(err, jobrepo.ErrDuplicateJob) {
		t.Fatalf("second CreateJob = %v, want ErrDuplicateJob", err)
	}
	msgID := int64(5)
	if err := jobs.TransitionJob(ctx, job.JobID, jobrepo.UploadCommitted, jobrepo.UploadAborted, jobrepo.UploadPatch{PutMsgID: &msgID}); err == nil {
		t.Fatal("a transition from the wrong prior state succeeded")
	}
	stored, err := jobs.GetJob(ctx, job.JobID)
	if err != nil || stored.State != jobrepo.UploadPrepared {
		t.Fatalf("job after a refused transition = %+v, %v", stored, err)
	}
}

func TestOperationLookup(t *testing.T) {
	ctx := context.Background()
	w := newRegistryWorld(t)
	lookup := &operationLookup{db: w.projectionDB}
	if _, found, err := lookup.LookupOperation(ctx, 7, testJobID, wire.OpDel); err != nil || found {
		t.Fatalf("missing operation = %v, %v; want found=false", found, err)
	}
	err := w.projectionDB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (7, ?1, 'del', 42, 'applied')`, testJobID)
		return err
	})
	if err != nil {
		t.Fatalf("insert operation: %v", err)
	}
	msgID, found, err := lookup.LookupOperation(ctx, 7, testJobID, wire.OpDel)
	if err != nil || !found || msgID != 42 {
		t.Fatalf("operation = %d, %v, %v; want 42", msgID, found, err)
	}
	if _, found, err := lookup.LookupOperation(ctx, 7, testJobID, wire.OpPut); err != nil || found {
		t.Fatalf("operation of another type = %v, %v; want found=false", found, err)
	}
}
