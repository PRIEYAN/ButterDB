package jobrepo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

func idempotencyRecordFor(job UploadJob, key string) IdempotencyRecord {
	return IdempotencyRecord{
		Scope:           job.Scope,
		IdempotencyKey:  key,
		BucketChannelID: job.BucketChannelID,
		ObjectKey:       job.ObjectKey,
		BodySHA256:      job.SHA256,
	}
}

func TestIdempotencyScoped(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	first := sampleUploadJob(jobsTestID(1))
	first.Scope, first.Origin = "device:phone", OriginAdmin
	second := sampleUploadJob(jobsTestID(2))
	second.Scope, second.Origin = "device:laptop", OriginAdmin
	if err := env.repo.CreateUploadWithIdempotency(ctx, first, idempotencyRecordFor(first, "same-key")); err != nil {
		t.Fatalf("first scope: %v", err)
	}
	if err := env.repo.CreateUploadWithIdempotency(ctx, second, idempotencyRecordFor(second, "same-key")); err != nil {
		t.Fatalf("second scope with the same key value: %v", err)
	}
	for _, job := range []UploadJob{first, second} {
		record, err := env.repo.GetIdempotency(ctx, job.Scope, "same-key")
		if err != nil {
			t.Fatalf("get %s: %v", job.Scope, err)
		}
		if record.JobID != job.JobID {
			t.Fatalf("scope %s resolved job %s, want %s", job.Scope, record.JobID, job.JobID)
		}
	}
}

func TestIdempotencyScopedPerAccessKey(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	alice := sampleUploadJob(jobsTestID(1))
	alice.Scope, alice.Writer = "key:AKIAALICE", "key.AKIAALICE"
	bob := sampleUploadJob(jobsTestID(2))
	bob.Scope, bob.Writer = "key:AKIABOB", "key.AKIABOB"
	bob.SHA256 = strings.Repeat("d", 64)
	if err := env.repo.CreateUploadWithIdempotency(ctx, alice, idempotencyRecordFor(alice, "retry-1")); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if err := env.repo.CreateUploadWithIdempotency(ctx, bob, idempotencyRecordFor(bob, "retry-1")); err != nil {
		t.Fatalf("bob reusing alice's header value: %v", err)
	}
	if _, err := env.repo.GetIdempotency(ctx, "key:AKIACAROL", "retry-1"); !errors.Is(err, ErrIdempotencyNotFound) {
		t.Fatalf("third access key saw a record: %v", err)
	}
	aliceRecord, _ := env.repo.GetIdempotency(ctx, alice.Scope, "retry-1")
	bobRecord, _ := env.repo.GetIdempotency(ctx, bob.Scope, "retry-1")
	if aliceRecord.JobID != alice.JobID || bobRecord.JobID != bob.JobID || aliceRecord.BodySHA256 == bobRecord.BodySHA256 {
		t.Fatalf("records crossed access keys: %+v %+v", aliceRecord, bobRecord)
	}
	if err := env.repo.CreateUploadWithIdempotency(ctx, sampleUploadJobWithScope(jobsTestID(3), alice.Scope), idempotencyRecordFor(alice, "retry-1")); !errors.Is(err, ErrDuplicateIdempotencyKey) {
		t.Fatalf("same access key repeating its header = %v, want ErrDuplicateIdempotencyKey", err)
	}
}

func sampleUploadJobWithScope(id, scope string) UploadJob {
	job := sampleUploadJob(id)
	job.Scope = scope
	return job
}

func TestIdempotencyDuplicateRollsBackTheJob(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	first := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, first, idempotencyRecordFor(first, "k")); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := sampleUploadJob(jobsTestID(2))
	if err := env.repo.CreateUploadWithIdempotency(ctx, second, idempotencyRecordFor(second, "k")); !errors.Is(err, ErrDuplicateIdempotencyKey) {
		t.Fatalf("second = %v", err)
	}
	if _, err := env.repo.GetUpload(ctx, second.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("job of a refused idempotency insert persisted: %v", err)
	}
}

func TestIdempotencyAbortedJobIsReplaced(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	first := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, first, idempotencyRecordFor(first, "k")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := env.repo.TransitionUpload(ctx, first.JobID, UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	second := sampleUploadJob(jobsTestID(2))
	if err := env.repo.CreateUploadWithIdempotency(ctx, second, idempotencyRecordFor(second, "k")); err != nil {
		t.Fatalf("replacement: %v", err)
	}
	record, err := env.repo.GetIdempotency(ctx, second.Scope, "k")
	if err != nil || record.JobID != second.JobID {
		t.Fatalf("record after replacement = %+v, %v", record, err)
	}
}

func TestIdempotencyExpiredRecordIsReplaced(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	first := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, first, idempotencyRecordFor(first, "k")); err != nil {
		t.Fatalf("first: %v", err)
	}
	env.clock.Advance(limits.IdempotencyTTL + time.Second)
	second := sampleUploadJob(jobsTestID(2))
	if err := env.repo.CreateUploadWithIdempotency(ctx, second, idempotencyRecordFor(second, "k")); err != nil {
		t.Fatalf("replacement of an expired record: %v", err)
	}
}

func TestIdempotencyRecordLifetime(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	job := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, job, idempotencyRecordFor(job, "k")); err != nil {
		t.Fatalf("create: %v", err)
	}
	record, err := env.repo.GetIdempotency(ctx, job.Scope, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.ExpiresAt-record.CreatedAt != limits.IdempotencyTTL.Milliseconds() {
		t.Fatalf("lifetime = %dms, want %dms", record.ExpiresAt-record.CreatedAt, limits.IdempotencyTTL.Milliseconds())
	}
}

func TestIdempotencyScopeMustMatchJob(t *testing.T) {
	env := newJobsEnv(t)
	job := sampleUploadJob(jobsTestID(1))
	record := idempotencyRecordFor(job, "k")
	record.Scope = "key:SOMEONEELSE"
	if err := env.repo.CreateUploadWithIdempotency(context.Background(), job, record); err == nil {
		t.Fatal("record scoped to another caller was accepted")
	}
	if _, err := env.repo.GetUpload(context.Background(), job.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("job persisted despite refused record: %v", err)
	}
}

func TestDeleteExpiredIdempotency(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	old := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, old, idempotencyRecordFor(old, "old")); err != nil {
		t.Fatalf("old: %v", err)
	}
	env.clock.Advance(23 * time.Hour)
	fresh := sampleUploadJob(jobsTestID(2))
	if err := env.repo.CreateUploadWithIdempotency(ctx, fresh, idempotencyRecordFor(fresh, "fresh")); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	env.clock.Advance(2 * time.Hour)
	removed, err := env.repo.DeleteExpiredIdempotency(ctx, env.clock.Now().UnixMilli())
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v; want 1", removed, err)
	}
	if _, err := env.repo.GetIdempotency(ctx, old.Scope, "old"); !errors.Is(err, ErrIdempotencyNotFound) {
		t.Fatalf("expired record still present: %v", err)
	}
	if _, err := env.repo.GetIdempotency(ctx, fresh.Scope, "fresh"); err != nil {
		t.Fatalf("unexpired record lost: %v", err)
	}
}

func TestPruneTerminalJobs(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	oldDone := sampleUploadJob(jobsTestID(1))
	env.mustCreateUpload(t, oldDone)
	env.mustCreateUpload(t, sampleUploadJob(jobsTestID(2)))
	env.mustCreateDelete(t, sampleDeleteJob(jobsTestID(3)))
	if err := env.repo.TransitionUpload(ctx, oldDone.JobID, UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort upload: %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, jobsTestID(3), DeletePrepared, DeleteAborted, DeletePatch{}); err != nil {
		t.Fatalf("abort delete: %v", err)
	}
	env.clock.Advance(8 * 24 * time.Hour)
	recentDone := sampleUploadJob(jobsTestID(4))
	env.mustCreateUpload(t, recentDone)
	if err := env.repo.TransitionUpload(ctx, recentDone.JobID, UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort recent: %v", err)
	}
	cutoff := env.clock.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	removed, err := env.repo.PruneTerminalJobs(ctx, cutoff)
	if err != nil || removed != 2 {
		t.Fatalf("pruned = %d, %v; want 2", removed, err)
	}
	if _, err := env.repo.GetUpload(ctx, jobsTestID(2)); err != nil {
		t.Fatalf("open job pruned: %v", err)
	}
	if _, err := env.repo.GetUpload(ctx, recentDone.JobID); err != nil {
		t.Fatalf("recent terminal job pruned: %v", err)
	}
	if _, err := env.repo.GetUpload(ctx, oldDone.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("old terminal job kept: %v", err)
	}
}

func TestPruneCascadesIdempotencyRecord(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	job := sampleUploadJob(jobsTestID(1))
	if err := env.repo.CreateUploadWithIdempotency(ctx, job, idempotencyRecordFor(job, "k")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := env.repo.TransitionUpload(ctx, job.JobID, UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	env.clock.Advance(time.Hour)
	if _, err := env.repo.PruneTerminalJobs(ctx, env.clock.Now().UnixMilli()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := env.repo.GetIdempotency(ctx, job.Scope, "k"); !errors.Is(err, ErrIdempotencyNotFound) {
		t.Fatalf("record outlived its job: %v", err)
	}
}
