package sweeper

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	base := newEnv(t).dependencies()
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"jobs", func(d *Dependencies) { d.Jobs = nil }},
		{"uploads", func(d *Dependencies) { d.Uploads = nil }},
		{"deletes", func(d *Dependencies) { d.Deletes = nil }},
		{"due cleanup", func(d *Dependencies) { d.Due = nil }},
		{"drainer", func(d *Dependencies) { d.Drainer = nil }},
		{"orphans", func(d *Dependencies) { d.Orphans = nil }},
		{"sleeper", func(d *Dependencies) { d.Sleeper = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := base
			tt.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want missing dependency", err)
			}
		})
	}
}

func TestSweeperFinishesUncertainPut(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	network, err := faketg.NewNetwork(e.clock)
	if err != nil {
		t.Fatalf("faketg: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: 11, FirstName: "A", Username: "a"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	peer, err := client.CreateChannel(ctx, "bucket", "")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	id := testJobID(1)
	header := "TDX1|t=put|j=" + id
	if err := e.jobs.CreateUpload(ctx, uploadJob(id)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.jobs.TransitionUpload(ctx, id, jobrepo.UploadPrepared, jobrepo.UploadPutAttempted, jobrepo.UploadPatch{PutHeader: strPtr(header)}); err != nil {
		t.Fatalf("put attempted: %v", err)
	}
	if err := client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}); err != nil {
		t.Fatalf("fault: %v", err)
	}
	request := tgclient.SendControlRequest{Peer: peer, Text: header, RandomID: 77}
	if msgID, err := client.SendControlIdempotent(ctx, request); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) || msgID != 0 {
		t.Fatalf("first send = %d, %v; want unknown outcome", msgID, err)
	}
	if err := e.jobs.TransitionUpload(ctx, id, jobrepo.UploadPutAttempted, jobrepo.UploadUncertainPut, jobrepo.UploadPatch{}); err != nil {
		t.Fatalf("uncertain: %v", err)
	}
	e.uploads.handle = func(ctx context.Context, jobID string) error {
		job, err := e.jobs.GetUpload(ctx, jobID)
		if err != nil {
			return err
		}
		msgID, err := client.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: job.PutHeader, RandomID: 77})
		if err != nil {
			return err
		}
		return e.jobs.TransitionUpload(ctx, jobID, jobrepo.UploadUncertainPut, jobrepo.UploadCommitted, jobrepo.UploadPatch{PutMsgID: &msgID})
	}

	e.clock.Advance(DefaultRecoveryAge - time.Second)
	e.sweeper.RunOnce(ctx)
	if got := e.uploads.called(); len(got) != 0 {
		t.Fatalf("job younger than the recovery age was resumed: %v", got)
	}
	e.clock.Advance(2 * time.Second)
	e.sweeper.RunOnce(ctx)
	if got := e.uploads.called(); !reflect.DeepEqual(got, []string{id}) {
		t.Fatalf("resumed = %v, want [%s]", got, id)
	}
	job, err := e.jobs.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if job.State != jobrepo.UploadCommitted || job.PutMsgID == 0 {
		t.Fatalf("job after sweep = %+v", job)
	}
	if controls := network.SentControls(peer.ChannelID); len(controls) != 1 || controls[0].MsgID != job.PutMsgID {
		t.Fatalf("telegram holds %+v, want exactly the original put at %d", controls, job.PutMsgID)
	}
	e.sweeper.RunOnce(ctx)
	if got := e.uploads.called(); len(got) != 1 {
		t.Fatalf("committed job resumed again: %v", got)
	}
}

func TestSweeperResumesDeleteJobs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.jobs.CreateDelete(ctx, deleteJob(testJobID(2))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.jobs.CreateDelete(ctx, deleteJob(testJobID(1))); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.clock.Advance(DefaultRecoveryAge + time.Second)
	e.sweeper.RunOnce(ctx)
	if got := e.deletes.called(); !reflect.DeepEqual(got, []string{testJobID(1), testJobID(2)}) {
		t.Fatalf("delete resumer calls = %v", got)
	}
	if got := e.uploads.called(); len(got) != 0 {
		t.Fatalf("upload resumer called for delete jobs: %v", got)
	}
}

func TestSweeperBacksOffFailingResume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := testJobID(1)
	if err := e.jobs.CreateUpload(ctx, uploadJob(id)); err != nil {
		t.Fatalf("create: %v", err)
	}
	fail := errors.New("telegram unreachable")
	e.uploads.handle = func(context.Context, string) error { return fail }
	e.clock.Advance(DefaultRecoveryAge)
	steps := []struct {
		advance time.Duration
		calls   int
	}{
		{0, 1},
		{30 * time.Second, 1},
		{31 * time.Second, 2},
		{time.Minute, 2},
		{61 * time.Second, 3},
		{3 * time.Minute, 3},
		{time.Minute + time.Second, 4},
	}
	for i, step := range steps {
		e.clock.Advance(step.advance)
		e.sweeper.RunOnce(ctx)
		if got := len(e.uploads.called()); got != step.calls {
			t.Fatalf("step %d: calls = %d, want %d", i, got, step.calls)
		}
	}
	e.uploads.handle = nil
	e.clock.Advance(time.Hour)
	e.sweeper.RunOnce(ctx)
	e.clock.Advance(time.Second)
	e.sweeper.RunOnce(ctx)
	if got := len(e.uploads.called()); got != 6 {
		t.Fatalf("calls after recovery = %d, want 6", got)
	}
}

func TestSweeperFailingJobDoesNotStarveOthers(t *testing.T) {
	e := newEnv(t)
	e.settings = Config{Batch: 1}
	e.build(t)
	ctx := context.Background()
	bad, good := testJobID(1), testJobID(2)
	if err := e.jobs.CreateUpload(ctx, uploadJob(bad)); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.clock.Advance(time.Second)
	if err := e.jobs.CreateUpload(ctx, uploadJob(good)); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.uploads.handle = func(_ context.Context, jobID string) error {
		if jobID == bad {
			return errors.New("stuck")
		}
		return nil
	}
	e.clock.Advance(DefaultRecoveryAge + time.Second)
	e.sweeper.RunOnce(ctx)
	e.sweeper.RunOnce(ctx)
	if got := e.uploads.called(); !reflect.DeepEqual(got, []string{bad, good}) {
		t.Fatalf("calls = %v, want the failing job then the healthy one", got)
	}
}

func TestSweeperRetriesPendingCleanupWithBackoff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	network, err := faketg.NewNetwork(e.clock)
	if err != nil {
		t.Fatalf("faketg: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: 11, FirstName: "A", Username: "a"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	peer, err := client.CreateChannel(ctx, "bucket", "")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	var seeded []int64
	for range 3 {
		msgID, err := network.SeedDocument(peer.ChannelID, "orphan", "TDX1|t=body", []byte("x"))
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		seeded = append(seeded, msgID)
	}
	for _, id := range seeded {
		e.exec(t, e.proj, "INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (?1, ?2, 'orphan_body')", peer.ChannelID, id)
	}
	if err := client.QueueFaults(faketg.MethodDeleteMessages,
		faketg.Fault{Kind: faketg.FaultError, Err: errors.New("rpc down")},
		faketg.Fault{Kind: faketg.FaultError, Err: errors.New("rpc down")}); err != nil {
		t.Fatalf("faults: %v", err)
	}
	drain := func(ctx context.Context, channelID int64) error {
		now := e.clock.Now().UnixMilli()
		batch, err := e.queue.ClaimCleanupBatch(ctx, channelID, now, limits.DeleteBatchSize)
		if err != nil || len(batch) == 0 {
			return err
		}
		if err := client.DeleteMessages(ctx, peer, batch); err != nil {
			return errors.Join(err, e.queue.DeferCleanup(ctx, channelID, batch, now, "delete failed"))
		}
		return e.queue.ConfirmCleanupDeleted(ctx, channelID, batch, now)
	}
	e.drainer.handle = drain

	steps := []struct {
		advance time.Duration
		drains  int
		pending int
	}{
		{0, 1, 3},
		{30 * time.Second, 1, 3},
		{31 * time.Second, 2, 3},
		{time.Minute, 2, 3},
		{time.Minute + time.Second, 3, 0},
	}
	for i, step := range steps {
		e.clock.Advance(step.advance)
		e.sweeper.RunOnce(ctx)
		if got := len(e.drainer.called()); got != step.drains {
			t.Fatalf("step %d: drain calls = %d, want %d", i, got, step.drains)
		}
		if got := e.count(t, e.proj, "SELECT count(*) FROM pending_message_cleanup"); got != step.pending {
			t.Fatalf("step %d: pending rows = %d, want %d", i, got, step.pending)
		}
	}
	if got := e.count(t, e.proj, "SELECT count(*) FROM deleted_messages WHERE source = 'cleanup'"); got != 3 {
		t.Fatalf("deleted_messages rows = %d, want 3", got)
	}
	if batches := network.DeletedBatches(peer.ChannelID); len(batches) != 1 || len(batches[0]) != 3 {
		t.Fatalf("telegram delete batches = %v, want one batch of three", batches)
	}
}

func TestSweeperDrainsOnlyDueChannels(t *testing.T) {
	e := newEnv(t)
	e.exec(t, e.proj, "INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (10, 1, 'deleted'), (20, 1, 'deleted')")
	e.exec(t, e.proj, "UPDATE pending_message_cleanup SET next_attempt_at = ?1 WHERE channel_id = 20", e.clock.Now().Add(time.Hour).UnixMilli())
	e.sweeper.RunOnce(context.Background())
	if got := e.drainer.called(); !reflect.DeepEqual(got, []int64{10}) {
		t.Fatalf("drained channels = %v, want [10]", got)
	}
}

func TestIdempotencyRowsExpire(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	job := uploadJob(testJobID(1))
	job.Scope, job.Origin = "device:phone", jobrepo.OriginAdmin
	record := jobrepo.IdempotencyRecord{Scope: job.Scope, IdempotencyKey: "retry", BucketChannelID: testChannel, ObjectKey: job.ObjectKey, BodySHA256: job.SHA256}
	if err := e.jobs.CreateUploadWithIdempotency(ctx, job, record); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM idempotency_keys"); got != 1 {
		t.Fatalf("fresh record count = %d, want 1", got)
	}
	e.clock.Advance(limits.IdempotencyTTL + time.Minute)
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM idempotency_keys"); got != 0 {
		t.Fatalf("expired record count = %d, want 0", got)
	}
}

func TestSweeperPrunesTerminalJobsAfterRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	done, open := testJobID(1), testJobID(2)
	for _, id := range []string{done, open} {
		if err := e.jobs.CreateUpload(ctx, uploadJob(id)); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := e.jobs.TransitionUpload(ctx, done, jobrepo.UploadPrepared, jobrepo.UploadAborted, jobrepo.UploadPatch{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	e.sweeper.RunOnce(ctx)
	e.clock.Advance(6 * 24 * time.Hour)
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM upload_jobs"); got != 2 {
		t.Fatalf("jobs before retention = %d, want 2", got)
	}
	e.clock.Advance(25 * time.Hour)
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM upload_jobs"); got != 1 {
		t.Fatalf("jobs after retention = %d, want only the open one", got)
	}
	if _, err := e.jobs.GetUpload(ctx, open); err != nil {
		t.Fatalf("open job pruned: %v", err)
	}
}

func TestSweeperMaintenanceRunsHourly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	job := uploadJob(testJobID(1))
	record := jobrepo.IdempotencyRecord{Scope: job.Scope, IdempotencyKey: "k", BucketChannelID: testChannel, ObjectKey: job.ObjectKey, BodySHA256: job.SHA256}
	if err := e.jobs.CreateUploadWithIdempotency(ctx, job, record); err != nil {
		t.Fatalf("create: %v", err)
	}
	e.sweeper.RunOnce(ctx)
	e.exec(t, e.control, "UPDATE idempotency_keys SET expires_at = created_at + 1")
	e.clock.Advance(30 * time.Minute)
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM idempotency_keys"); got != 1 {
		t.Fatalf("maintenance ran before the hour: %d rows", got)
	}
	e.clock.Advance(31 * time.Minute)
	e.sweeper.RunOnce(ctx)
	if got := e.count(t, e.control, "SELECT count(*) FROM idempotency_keys"); got != 0 {
		t.Fatalf("maintenance did not run after the hour: %d rows", got)
	}
}

func TestSweeperQueuesOrphanBodiesAfterGrace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owned := testJobID(1)
	if err := e.jobs.CreateUpload(ctx, uploadJob(owned)); err != nil {
		t.Fatalf("create: %v", err)
	}
	old := testStart.Add(-48 * time.Hour).Unix()
	young := testStart.Add(-time.Hour).Unix()
	insert := "INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (?1, ?2, ?3, 3, 0, ?4)"
	e.exec(t, e.proj, insert, testChannel, 10, testJobID(7), old)
	e.exec(t, e.proj, insert, testChannel, 11, testJobID(7), young)
	e.exec(t, e.proj, insert, testChannel, 12, owned, old)
	e.exec(t, e.proj, insert, testChannel, 13, testJobID(8), old)
	e.exec(t, e.proj, "INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256, content_type, encrypted, writer, op_ts, msg_date) VALUES (?1, 'k', 14, 13, ?2, 3, 3, ?3, ?4, 'text/plain', 0, 'w', 1, 1)",
		testChannel, testJobID(8), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	e.sweeper.RunOnce(ctx)
	rows := map[int64]string{}
	err := e.proj.Read(ctx, func(tx *sql.Tx) error {
		r, err := tx.QueryContext(ctx, "SELECT msg_id, reason FROM pending_message_cleanup WHERE channel_id = ?1", testChannel)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var id int64
			var reason string
			if err := r.Scan(&id, &reason); err != nil {
				return err
			}
			rows[id] = reason
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	if want := map[int64]string{10: "orphan_body"}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("queued = %v, want %v (young, job-owned and live bodies stay)", rows, want)
	}
}

func TestSweeperNeverHoldsTransactionAcrossResume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.jobs.CreateUpload(ctx, uploadJob(testJobID(1))); err != nil {
		t.Fatalf("create: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	e.uploads.handle = func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.clock.Advance(DefaultRecoveryAge + time.Second)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.sweeper.RunOnce(ctx)
	}()
	<-entered
	started := time.Now()
	if err := e.jobs.CreateUpload(ctx, uploadJob(testJobID(2))); err != nil {
		t.Fatalf("write during blocked resume: %v", err)
	}
	if err := e.jobs.CreateDelete(ctx, deleteJob(testJobID(3))); err != nil {
		t.Fatalf("write during blocked resume: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("writes took %v while a resume was blocked", elapsed)
	}
	close(release)
	wg.Wait()
}

func TestSweeperLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.jobs.CreateUpload(ctx, uploadJob(testJobID(1))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.sweeper.Close(ctx); err != nil {
		t.Fatalf("close before start: %v", err)
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	if err := e.sweeper.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	cancelStart()
	if err := e.sweeper.Start(ctx); err == nil {
		t.Fatal("second start succeeded")
	}
	select {
	case d := <-e.sleeper.slept:
		if d != DefaultInterval {
			t.Fatalf("slept %v, want the default interval", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup pass never finished")
	}
	if got := e.uploads.called(); len(got) != 1 {
		t.Fatalf("startup pass resumed %v, want the job from before the restart regardless of age", got)
	}
	e.sleeper.release <- struct{}{}
	select {
	case <-e.sleeper.slept:
	case <-time.After(5 * time.Second):
		t.Fatal("second pass never ran")
	}
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := e.sweeper.Close(closeCtx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := e.sweeper.Close(closeCtx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	select {
	case <-e.sweeper.done:
	default:
		t.Fatal("loop goroutine outlived Close")
	}
}
