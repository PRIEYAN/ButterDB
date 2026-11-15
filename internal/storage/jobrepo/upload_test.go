package jobrepo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestNewRejectsNilDependencies(t *testing.T) {
	env := newJobsEnv(t)
	tests := []struct {
		name string
		db   Database
		clk  clock.Clock
	}{
		{"nil database", nil, env.clock},
		{"nil clock", env.db, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.db, tt.clk); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want missing dependency", err)
			}
		})
	}
}

func TestCreateAndGetUpload(t *testing.T) {
	env := newJobsEnv(t)
	job := sampleEncryptedUploadJob(jobsTestID(1))
	job.UserMetaJSON = `{"k":"v"}`
	job.IfNoneMatch = true
	job.BasePutMsgID = 7
	env.mustCreateUpload(t, job)
	got, err := env.repo.GetUpload(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("get upload: %v", err)
	}
	if got.State != UploadPrepared || !got.Encrypted || string(got.FileSalt) != string(job.FileSalt) || string(got.NoncePrefix) != string(job.NoncePrefix) {
		t.Fatalf("stored job lost encryption parameters: %+v", got)
	}
	if got.UserMetaJSON != `{"k":"v"}` || got.SystemMetaJSON != "{}" || !got.IfNoneMatch || got.BasePutMsgID != 7 {
		t.Fatalf("stored job lost fields: %+v", got)
	}
	if got.CreatedAt != jobsTestStart.UnixMilli() || got.UpdatedAt != got.CreatedAt {
		t.Fatalf("timestamps = %d, %d", got.CreatedAt, got.UpdatedAt)
	}
}

func TestCreateUploadRejections(t *testing.T) {
	env := newJobsEnv(t)
	env.mustCreateUpload(t, sampleUploadJob(jobsTestID(1)))
	started := sampleUploadJob(jobsTestID(2))
	started.State = UploadBodySending
	badScope := sampleUploadJob(jobsTestID(3))
	badScope.Scope = "somebody"
	tests := []struct {
		name string
		job  UploadJob
		want error
	}{
		{"duplicate job id", sampleUploadJob(jobsTestID(1)), ErrDuplicateJob},
		{"job not starting prepared", started, ErrInvalidTransition},
		{"scope without prefix", badScope, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := env.repo.CreateUpload(context.Background(), tt.job)
			if err == nil {
				t.Fatal("create succeeded, want error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGetUploadMissing(t *testing.T) {
	env := newJobsEnv(t)
	if _, err := env.repo.GetUpload(context.Background(), jobsTestID(9)); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("get missing = %v, want ErrJobNotFound", err)
	}
}

func TestTransitionCAS(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	ctx := context.Background()
	env.clock.Advance(time.Second)
	if err := env.repo.TransitionUpload(ctx, id, UploadPrepared, UploadBodySending, UploadPatch{IncrementAttempts: true}); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	tests := []struct {
		name string
		id   string
		from UploadState
		to   UploadState
		want error
	}{
		{"stale prior state", id, UploadPrepared, UploadAborted, ErrStaleTransition},
		{"stale prior state repeating the winner", id, UploadPrepared, UploadBodySending, ErrStaleTransition},
		{"missing job", jobsTestID(2), UploadPrepared, UploadBodySending, ErrJobNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := env.repo.TransitionUpload(ctx, tt.id, tt.from, tt.to, UploadPatch{ErrorCode: strPtr("loser")})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
	got, err := env.repo.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != UploadBodySending || got.ErrorCode != "" || got.Attempts != 1 {
		t.Fatalf("losing transitions mutated the row: %+v", got)
	}
	if got.UpdatedAt != jobsTestStart.Add(time.Second).UnixMilli() {
		t.Fatalf("updated_at = %d, want the winning transition time", got.UpdatedAt)
	}
}

func TestTransitionCASConcurrentWinners(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	const racers = 8
	results := make(chan error, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- env.repo.TransitionUpload(context.Background(), id, UploadPrepared, UploadBodySending, UploadPatch{})
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, ErrStaleTransition):
			t.Fatalf("loser error = %v, want stale transition", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
}

func TestUploadTransitionTable(t *testing.T) {
	allowed := map[UploadState][]UploadState{
		UploadPrepared:      {UploadBodySending, UploadPutAttempted, UploadAborted},
		UploadBodySending:   {UploadBodySent, UploadUncertainBody, UploadAborted},
		UploadUncertainBody: {UploadBodySent, UploadAborted},
		UploadBodySent:      {UploadPutAttempted, UploadAborted},
		UploadPutAttempted:  {UploadCommitted, UploadUncertainPut},
		UploadUncertainPut:  {UploadCommitted, UploadPutAttempted, UploadAborted},
	}
	states := []UploadState{UploadPrepared, UploadBodySending, UploadUncertainBody, UploadBodySent, UploadPutAttempted, UploadUncertainPut, UploadCommitted, UploadAborted}
	for _, from := range states {
		for _, to := range states {
			name := string(from) + " to " + string(to)
			t.Run(name, func(t *testing.T) {
				want := false
				for _, target := range allowed[from] {
					want = want || target == to
				}
				err := ValidateUploadTransition(from, to)
				if want && err != nil {
					t.Fatalf("diagram edge rejected: %v", err)
				}
				if !want && !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("error = %v, want ErrInvalidTransition", err)
				}
			})
		}
	}
	if err := ValidateUploadTransition("bogus", UploadAborted); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unknown source state = %v", err)
	}
}

func TestInvalidTransitionDoesNotMutate(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	env.clock.Advance(time.Minute)
	err := env.repo.TransitionUpload(context.Background(), id, UploadPrepared, UploadCommitted, UploadPatch{ErrorCode: strPtr("x"), IncrementAttempts: true})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	got, err := env.repo.GetUpload(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != UploadPrepared || got.Attempts != 0 || got.ErrorCode != "" || got.UpdatedAt != got.CreatedAt {
		t.Fatalf("invalid transition mutated the row: %+v", got)
	}
}

func TestUploadHappyPathPatches(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	ctx := context.Background()
	steps := []struct {
		from, to UploadState
		patch    UploadPatch
	}{
		{UploadPrepared, UploadBodySending, UploadPatch{}},
		{UploadBodySending, UploadBodySent, UploadPatch{BodyMsgID: int64Ptr(55)}},
		{UploadBodySent, UploadPutAttempted, UploadPatch{PutHeader: strPtr("TDX1|t=put|j=" + id)}},
		{UploadPutAttempted, UploadCommitted, UploadPatch{PutMsgID: int64Ptr(56)}},
	}
	for _, step := range steps {
		if err := env.repo.TransitionUpload(ctx, id, step.from, step.to, step.patch); err != nil {
			t.Fatalf("%s to %s: %v", step.from, step.to, err)
		}
	}
	got, err := env.repo.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != UploadCommitted || got.BodyMsgID != 55 || got.PutMsgID != 56 || !strings.HasPrefix(got.PutHeader, "TDX1|t=put") {
		t.Fatalf("patches not applied: %+v", got)
	}
}

func TestTableChecksGuardStateInvariants(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	ctx := context.Background()
	if err := env.repo.TransitionUpload(ctx, id, UploadPrepared, UploadPutAttempted, UploadPatch{}); err == nil {
		t.Fatal("put_attempted without a put header was accepted")
	}
	got, err := env.repo.GetUpload(ctx, id)
	if err != nil || got.State != UploadPrepared {
		t.Fatalf("row after refused transition = %+v, %v", got, err)
	}
}

func TestPatchUploadKeepsStateAndChecksIt(t *testing.T) {
	env := newJobsEnv(t)
	id := jobsTestID(1)
	env.mustCreateUpload(t, sampleUploadJob(id))
	ctx := context.Background()
	if err := env.repo.PatchUpload(ctx, id, UploadPrepared, UploadPatch{ErrorCode: strPtr("internal.error"), IncrementAttempts: true}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if err := env.repo.PatchUpload(ctx, id, UploadBodySending, UploadPatch{IncrementAttempts: true}); !errors.Is(err, ErrStaleTransition) {
		t.Fatalf("patch in the wrong state = %v, want stale", err)
	}
	got, _ := env.repo.GetUpload(ctx, id)
	if got.State != UploadPrepared || got.Attempts != 1 || got.ErrorCode != "internal.error" {
		t.Fatalf("patched row = %+v", got)
	}
}

func TestEncryptedJobNeedsSaltAndNonce(t *testing.T) {
	env := newJobsEnv(t)
	withSalt := func(salt, nonce []byte) UploadJob {
		job := sampleEncryptedUploadJob(jobsTestID(1))
		job.FileSalt, job.NoncePrefix = salt, nonce
		return job
	}
	plainWithSalt := sampleUploadJob(jobsTestID(1))
	plainWithSalt.FileSalt = []byte(strings.Repeat("s", 16))
	tests := []struct {
		name string
		job  UploadJob
		ok   bool
	}{
		{"salt and prefix of the right size", withSalt([]byte(strings.Repeat("s", 16)), []byte(strings.Repeat("n", 20))), true},
		{"no salt", withSalt(nil, []byte(strings.Repeat("n", 20))), false},
		{"no nonce prefix", withSalt([]byte(strings.Repeat("s", 16)), nil), false},
		{"short salt", withSalt([]byte(strings.Repeat("s", 15)), []byte(strings.Repeat("n", 20))), false},
		{"long nonce prefix", withSalt([]byte(strings.Repeat("s", 16)), []byte(strings.Repeat("n", 21))), false},
		{"unencrypted job with a salt", plainWithSalt, false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.job.JobID = jobsTestID(i + 1)
			err := env.repo.CreateUpload(context.Background(), tt.job)
			if tt.ok && err != nil {
				t.Fatalf("create: %v", err)
			}
			if !tt.ok {
				if err == nil {
					t.Fatal("create succeeded, want the CHECK constraint to refuse it")
				}
				if _, getErr := env.repo.GetUpload(context.Background(), tt.job.JobID); !errors.Is(getErr, ErrJobNotFound) {
					t.Fatalf("refused row exists: %v", getErr)
				}
			}
		})
	}
}

func TestListRecoverableUploadsStableOrder(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	ids := []string{jobsTestID(3), jobsTestID(1), jobsTestID(2), jobsTestID(4), jobsTestID(5)}
	for i, id := range ids {
		if i == 3 {
			env.clock.Advance(time.Minute)
		}
		env.mustCreateUpload(t, sampleUploadJob(id))
	}
	if err := env.repo.TransitionUpload(ctx, ids[0], UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	cutoff := jobsTestStart.UnixMilli()
	got, err := env.repo.ListRecoverableUploads(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{jobsTestID(1), jobsTestID(2)}
	if len(got) != len(want) || got[0].JobID != want[0] || got[1].JobID != want[1] {
		t.Fatalf("recoverable before cutoff = %v, want %v", jobIDsOf(got), want)
	}
	all, err := env.repo.ListRecoverableUploads(ctx, cutoff+time.Hour.Milliseconds(), 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 || all[2].JobID != jobsTestID(4) {
		t.Fatalf("limited list = %v", jobIDsOf(all))
	}
}

func jobIDsOf(jobs []UploadJob) []string {
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.JobID
	}
	return ids
}

func TestCorruptUploadRowDetected(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{"unknown state", "UPDATE upload_jobs SET state = 'zombie'"},
		{"unknown kind", "UPDATE upload_jobs SET kind = 'teleport'"},
		{"malformed scope", "UPDATE upload_jobs SET scope = 'nobody'"},
		{"encrypted without salt", "UPDATE upload_jobs SET encrypted = 1, encryption_version = 1, key_id = 'cccccccccccccccc'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newJobsEnv(t)
			id := jobsTestID(1)
			env.mustCreateUpload(t, sampleUploadJob(id))
			env.rawExec(t, "PRAGMA ignore_check_constraints = 1")
			env.rawExec(t, tt.statement)
			if _, err := env.repo.GetUpload(context.Background(), id); !errors.Is(err, ErrCorruptRow) {
				t.Fatalf("get corrupt row = %v, want ErrCorruptRow", err)
			}
			if _, err := env.repo.ListRecoverableUploads(context.Background(), jobsTestStart.UnixMilli()+1, 10); !errors.Is(err, ErrCorruptRow) {
				t.Fatalf("list with corrupt row = %v, want ErrCorruptRow", err)
			}
		})
	}
}

func TestOpenUploadJobIDs(t *testing.T) {
	env := newJobsEnv(t)
	ctx := context.Background()
	env.mustCreateUpload(t, sampleUploadJob(jobsTestID(1)))
	env.mustCreateUpload(t, sampleUploadJob(jobsTestID(2)))
	if err := env.repo.TransitionUpload(ctx, jobsTestID(2), UploadPrepared, UploadAborted, UploadPatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	open, err := env.repo.OpenUploadJobIDs(ctx, []string{jobsTestID(1), jobsTestID(2), jobsTestID(3)})
	if err != nil {
		t.Fatalf("open ids: %v", err)
	}
	if _, ok := open[jobsTestID(1)]; !ok || len(open) != 1 {
		t.Fatalf("open = %v, want only job 1", open)
	}
}

func TestNoTxAcrossNetwork(t *testing.T) {
	env := newJobsEnv(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 16)
	network, err := faketg.NewNetwork(env.clock, faketg.WithDelay(func(ctx context.Context, _ time.Duration) error {
		entered <- struct{}{}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err != nil {
		t.Fatalf("faketg network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: 11, FirstName: "A", Username: "a"})
	if err != nil {
		t.Fatalf("faketg account: %v", err)
	}
	peer, err := client.CreateChannel(context.Background(), "bucket", "")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := client.SetLatency(faketg.MethodSendControlIdempotent, 2*time.Second); err != nil {
		t.Fatalf("set latency: %v", err)
	}
	const jobs = 8
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := range jobs {
		id := jobsTestID(i + 1)
		env.mustCreateUpload(t, sampleUploadJob(id))
		wg.Add(1)
		go func() {
			defer wg.Done()
			header := strPtr("TDX1|t=put|j=" + id)
			if err := env.repo.TransitionUpload(ctx, id, UploadPrepared, UploadPutAttempted, UploadPatch{PutHeader: header}); err != nil {
				t.Errorf("mark put attempted: %v", err)
				return
			}
			_, _ = client.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: *header, RandomID: int64(i + 1)})
			_ = env.repo.PatchUpload(context.Background(), id, UploadPutAttempted, UploadPatch{IncrementAttempts: true})
		}()
	}
	for range jobs {
		<-entered
	}
	started := time.Now()
	var writers sync.WaitGroup
	for i := range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			id := jobsTestID(100 + i)
			if err := env.repo.CreateUpload(context.Background(), sampleUploadJob(id)); err != nil {
				t.Errorf("create while sends are in flight: %v", err)
			}
		}()
	}
	writers.Wait()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("4 concurrent writes took %v while %d sends were blocked, want under 100ms", elapsed, jobs)
	}
	close(gate)
	wg.Wait()
}
