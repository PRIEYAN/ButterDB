package jobrepo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeleteTransitionTable(t *testing.T) {
	allowed := map[DeleteState][]DeleteState{
		DeletePrepared:     {DeleteDelAttempted, DeleteAborted},
		DeleteDelAttempted: {DeleteCommitted, DeleteUncertainDel},
		DeleteUncertainDel: {DeleteCommitted, DeleteDelAttempted, DeleteAborted},
	}
	states := []DeleteState{DeletePrepared, DeleteDelAttempted, DeleteUncertainDel, DeleteCommitted, DeleteAborted}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				want := false
				for _, target := range allowed[from] {
					want = want || target == to
				}
				err := ValidateDeleteTransition(from, to)
				if want && err != nil {
					t.Fatalf("diagram edge rejected: %v", err)
				}
				if !want && !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("error = %v, want ErrInvalidTransition", err)
				}
			})
		}
	}
}

func TestDeleteJobLifecycle(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	id := jobsTestID(1)
	env.mustCreateDelete(t, sampleDeleteJob(id))
	if err := env.repo.CreateDelete(ctx, sampleDeleteJob(id)); !errors.Is(err, ErrDuplicateJob) {
		t.Fatalf("duplicate create = %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, id, DeletePrepared, DeleteDelAttempted, DeletePatch{DelHeader: strPtr("TDX1|t=del|j=" + id)}); err != nil {
		t.Fatalf("mark del attempted: %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, id, DeletePrepared, DeleteAborted, DeletePatch{}); !errors.Is(err, ErrStaleTransition) {
		t.Fatalf("stale transition = %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, id, DeleteDelAttempted, DeletePrepared, DeletePatch{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("backwards transition = %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, jobsTestID(2), DeletePrepared, DeleteAborted, DeletePatch{}); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("missing job = %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, id, DeleteDelAttempted, DeleteCommitted, DeletePatch{DelMsgID: int64Ptr(90), IncrementAttempts: true}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := env.repo.GetDelete(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != DeleteCommitted || got.DelMsgID != 90 || got.Attempts != 1 || got.TargetPutMsgID != 42 {
		t.Fatalf("stored delete = %+v", got)
	}
}

func TestDeleteRejectsCommittedWithoutTombstoneMessage(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateDelete(t, sampleDeleteJob(id))
	ctx := context.Background()
	if err := env.repo.TransitionDelete(ctx, id, DeletePrepared, DeleteDelAttempted, DeletePatch{DelHeader: strPtr("h")}); err != nil {
		t.Fatalf("mark del attempted: %v", err)
	}
	if err := env.repo.TransitionDelete(ctx, id, DeleteDelAttempted, DeleteCommitted, DeletePatch{}); err == nil {
		t.Fatal("committed without a tombstone message id was accepted")
	}
}

func TestListRecoverableDeletesStableOrder(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	env.mustCreateDelete(t, sampleDeleteJob(jobsTestID(2)))
	env.mustCreateDelete(t, sampleDeleteJob(jobsTestID(1)))
	env.clock.Advance(time.Minute)
	env.mustCreateDelete(t, sampleDeleteJob(jobsTestID(3)))
	env.mustCreateDelete(t, sampleDeleteJob(jobsTestID(4)))
	if err := env.repo.TransitionDelete(ctx, jobsTestID(4), DeletePrepared, DeleteAborted, DeletePatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	got, err := env.repo.ListRecoverableDeletes(ctx, jobsTestStart.UnixMilli()+time.Hour.Milliseconds(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{jobsTestID(1), jobsTestID(2), jobsTestID(3)}
	if len(got) != len(want) {
		t.Fatalf("recoverable = %d rows, want %d", len(got), len(want))
	}
	for i, job := range got {
		if job.JobID != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, job.JobID, want[i])
		}
	}
}

func TestCorruptDeleteRowDetected(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{"unknown state", "UPDATE delete_jobs SET state = 'zombie'"},
		{"unknown origin", "UPDATE delete_jobs SET origin = 'carrier pigeon'"},
		{"missing target", "UPDATE delete_jobs SET target_put_msg_id = 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newJobsEnv(t)
			id := jobsTestID(1)
			env.mustCreateDelete(t, sampleDeleteJob(id))
			env.rawExec(t, "PRAGMA ignore_check_constraints = 1")
			env.rawExec(t, tt.statement)
			if _, err := env.repo.GetDelete(context.Background(), id); !errors.Is(err, ErrCorruptRow) {
				t.Fatalf("get corrupt row = %v, want ErrCorruptRow", err)
			}
		})
	}
}
