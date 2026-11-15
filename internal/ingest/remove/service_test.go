package remove

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestTombstoneBeforeDelete(t *testing.T) {
	e := newEnv(t)
	seed := e.seedObject("docs/a.txt")
	if err := e.svc.Delete(e.ctx, bucketName, "docs/a.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	events := e.log.all()
	want := []string{"send", "delete:2:tombstone-projected"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if e.liveObjects() != 0 {
		t.Fatalf("object still live after delete")
	}
	for _, id := range []int64{seed.BodyMsgID, seed.PutMsgID} {
		if e.channelHas(id) {
			t.Fatalf("message %d still in the channel", id)
		}
	}
	if e.pending() != 0 || e.deletedByCleanup() != 2 {
		t.Fatalf("pending = %d, cleanup-deleted = %d, want 0 and 2", e.pending(), e.deletedByCleanup())
	}
	job := e.jobs.only(t)
	if job.State != jobrepo.DeleteCommitted || job.DelMsgID == 0 || job.TargetPutMsgID != seed.PutMsgID || job.DelHeader == "" {
		t.Fatalf("job = %+v", job)
	}
	if !slices.Equal(e.jobs.history, []jobrepo.DeleteState{jobrepo.DeletePrepared, jobrepo.DeleteDelAttempted, jobrepo.DeleteCommitted}) {
		t.Fatalf("job history = %v", e.jobs.history)
	}
	if !e.channelHas(job.DelMsgID) {
		t.Fatalf("tombstone message %d must stay in the channel", job.DelMsgID)
	}
	if e.gates.acquired != 1 || e.gates.released != 1 || e.locks.anyHeld() {
		t.Fatalf("gate acquired %d released %d, key lock held %v", e.gates.acquired, e.gates.released, e.locks.anyHeld())
	}
}

func TestTombstoneBlocksResurrection(t *testing.T) {
	e := newEnv(t)
	seed := e.seedObject("k")
	if err := e.svc.Delete(e.ctx, bucketName, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	job := e.jobs.only(t)
	if job.DelMsgID <= seed.PutMsgID {
		t.Fatalf("tombstone id %d not above put id %d", job.DelMsgID, seed.PutMsgID)
	}
	if got := e.count(`SELECT del_msg_id FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'k'`, e.peer.ChannelID); int64(got) != job.DelMsgID {
		t.Fatalf("tombstone row = %d, want %d", got, job.DelMsgID)
	}
}

func TestMissingKeySucceeds(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.Delete(e.ctx, bucketName, "absent"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if e.jobs.count() != 0 {
		t.Fatalf("jobs = %d, want 0", e.jobs.count())
	}
	if events := e.log.all(); len(events) != 0 {
		t.Fatalf("telegram traffic for a missing key: %v", events)
	}
	if e.client.CallCount(faketg.MethodSendControlIdempotent) != 0 || e.client.CallCount(faketg.MethodDeleteMessages) != 0 {
		t.Fatalf("telegram called for a missing key")
	}
}

func TestDeleteRejections(t *testing.T) {
	cases := []struct {
		name   string
		bucket bucket.BucketName
		key    object.Key
		setup  func(e *env, deps *Dependencies)
		want   error
	}{
		{"unknown bucket", "nowhere", "k", nil, errs.ErrNoSuchBucket},
		{"empty key", bucketName, "", nil, object.ErrInvalidKey},
		{"key with control character", bucketName, "a\x01b", nil, object.ErrInvalidKey},
		{"no write authority", bucketName, "k", func(_ *env, deps *Dependencies) {
			deps.Authority = openAuthority{err: errs.ErrSlowDown}
		}, errs.ErrSlowDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedObject("k")
			deps := e.deps(e.drainer)
			if tc.setup != nil {
				tc.setup(e, &deps)
			}
			svc, err := New(deps)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = svc.Delete(e.ctx, tc.bucket, tc.key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Delete error = %v, want %v", err, tc.want)
			}
			if e.jobs.count() != 0 || e.liveObjects() != 1 {
				t.Fatalf("rejected delete changed state: jobs %d, live %d", e.jobs.count(), e.liveObjects())
			}
			if e.gates.acquired != e.gates.released {
				t.Fatalf("gate leaked: acquired %d released %d", e.gates.acquired, e.gates.released)
			}
		})
	}
}

func TestInvalidWriterPersistsNothing(t *testing.T) {
	e := newEnv(t)
	e.seedObject("k")
	deps := e.deps(e.drainer)
	deps.Principals = fixedPrincipal{p: Principal{Origin: jobrepo.OriginS3, Scope: "key:AKIATEST", Writer: "bad writer"}}
	svc, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Delete(e.ctx, bucketName, "k"); err == nil {
		t.Fatalf("Delete accepted an invalid writer")
	}
	if e.jobs.count() != 0 || e.client.CallCount(faketg.MethodSendControlIdempotent) != 1 {
		t.Fatalf("jobs %d, sends %d", e.jobs.count(), e.client.CallCount(faketg.MethodSendControlIdempotent))
	}
}

func TestUncertainTombstoneSlowDown(t *testing.T) {
	cases := []struct {
		name      string
		delivered bool
	}{
		{"tombstone never delivered", false},
		{"tombstone delivered but receipt lost", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			seed := e.seedObject("k")
			if err := e.client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: tc.delivered}); err != nil {
				t.Fatalf("queue fault: %v", err)
			}
			err := e.svc.Delete(e.ctx, bucketName, "k")
			if !errors.Is(err, errs.ErrSlowDown) {
				t.Fatalf("Delete error = %v, want SlowDown", err)
			}
			job := e.jobs.only(t)
			if job.State != jobrepo.DeleteUncertainDel || job.DelHeader == "" || job.ErrorCode == "" {
				t.Fatalf("job = %+v", job)
			}
			if e.liveObjects() != 1 || e.client.CallCount(faketg.MethodDeleteMessages) != 0 {
				t.Fatalf("nothing may be deleted before the tombstone is projected")
			}
			if !e.channelHas(seed.PutMsgID) || !e.channelHas(seed.BodyMsgID) {
				t.Fatalf("object messages lost on an unknown tombstone")
			}

			if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			done := e.jobs.only(t)
			if done.State != jobrepo.DeleteCommitted || done.Attempts != 1 {
				t.Fatalf("resumed job = %+v", done)
			}
			if e.liveObjects() != 0 || e.pending() != 0 || e.channelHas(seed.PutMsgID) {
				t.Fatalf("resumed delete incomplete: live %d pending %d", e.liveObjects(), e.pending())
			}
			tombstones := 0
			controls := e.net.SentControls(e.peer.ChannelID)
			for _, sent := range controls {
				if strings.Contains(sent.Text, "t=del") {
					tombstones++
				}
			}
			if tombstones != 1 {
				t.Fatalf("tombstones in channel = %d, want 1 (same random id must deduplicate)", tombstones)
			}
		})
	}
}

func TestResumeAfterCrashMidBatch(t *testing.T) {
	t.Run("crash after projection before the job is marked committed", func(t *testing.T) {
		e := newEnv(t)
		seed := e.seedObject("k")
		e.jobs.failCommit = errors.New("disk full")
		if err := e.svc.Delete(e.ctx, bucketName, "k"); err == nil {
			t.Fatalf("Delete succeeded although the job could not be committed")
		}
		job := e.jobs.only(t)
		if job.State != jobrepo.DeleteDelAttempted {
			t.Fatalf("job state = %s", job.State)
		}
		if e.client.CallCount(faketg.MethodDeleteMessages) != 0 || e.pending() != 2 {
			t.Fatalf("no message may be deleted before the job commits: deletes %d pending %d", e.client.CallCount(faketg.MethodDeleteMessages), e.pending())
		}
		e.jobs.failCommit = nil
		if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		done := e.jobs.only(t)
		if done.State != jobrepo.DeleteCommitted || done.DelMsgID == 0 {
			t.Fatalf("job = %+v", done)
		}
		if e.client.CallCount(faketg.MethodSendControlIdempotent) != 1+1 {
			t.Fatalf("resume must not resend a projected tombstone: sends %d", e.client.CallCount(faketg.MethodSendControlIdempotent))
		}
		if e.channelHas(seed.PutMsgID) || e.pending() != 0 {
			t.Fatalf("messages not deleted after resume")
		}
	})
	t.Run("crash after del_attempted before the send", func(t *testing.T) {
		e := newEnv(t)
		seed := e.seedObject("k")
		e.crashAfterAttempt(t, "k", seed.PutMsgID)
		job := e.jobs.only(t)
		if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if got := e.jobs.only(t); got.State != jobrepo.DeleteCommitted {
			t.Fatalf("job = %+v", got)
		}
		if e.liveObjects() != 0 || e.channelHas(seed.PutMsgID) || e.channelHas(seed.BodyMsgID) {
			t.Fatalf("object not deleted by resume")
		}
	})
	t.Run("crash after prepared before del_attempted", func(t *testing.T) {
		e := newEnv(t)
		seed := e.seedObject("k")
		e.insertJob(t, "k", seed.PutMsgID, jobrepo.DeletePrepared, "")
		job := e.jobs.only(t)
		if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if got := e.jobs.only(t); got.State != jobrepo.DeleteCommitted || got.DelHeader == "" {
			t.Fatalf("job = %+v", got)
		}
		if e.liveObjects() != 0 {
			t.Fatalf("object still live")
		}
	})
	t.Run("batch failure leaves the plan queued and a later drain finishes it", func(t *testing.T) {
		e := newEnv(t)
		seed := e.seedObject("k")
		pad := e.queuePad(120)
		if err := e.client.FaultOnCall(faketg.MethodDeleteMessages, 1, faketg.Fault{Kind: faketg.FaultError, Err: errors.New("connection reset")}); err != nil {
			t.Fatalf("fault: %v", err)
		}
		if err := e.svc.Delete(e.ctx, bucketName, "k"); err != nil {
			t.Fatalf("Delete must answer success when only a batch failed: %v", err)
		}
		if e.jobs.only(t).State != jobrepo.DeleteCommitted || e.liveObjects() != 0 {
			t.Fatalf("tombstone must stay committed after a failed batch")
		}
		if e.pending() != 100 {
			t.Fatalf("pending after first failed batch = %d, want 100 (second batch of 22 deleted)", e.pending())
		}
		if got := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE last_error <> ''`); got != 100 {
			t.Fatalf("rows carrying the failure = %d, want 100", got)
		}
		restarted := e.newDrainer(&sqlQueue{db: e.db, lease: true}, e.client)
		if err := restarted.DrainChannel(e.ctx, e.peer.ChannelID); err != nil {
			t.Fatalf("DrainChannel after restart: %v", err)
		}
		if e.pending() != 0 {
			t.Fatalf("pending after restart drain = %d", e.pending())
		}
		for _, id := range append([]int64{seed.PutMsgID, seed.BodyMsgID}, pad...) {
			if e.channelHas(id) {
				t.Fatalf("message %d survived", id)
			}
		}
		if e.deletedByCleanup() != 122 {
			t.Fatalf("deleted_messages from cleanup = %d, want 122", e.deletedByCleanup())
		}
	})
}

func (e *env) insertJob(t *testing.T, key string, target int64, state jobrepo.DeleteState, header string) {
	t.Helper()
	job := jobrepo.DeleteJob{
		JobID: e.jobID(), Origin: jobrepo.OriginS3, Scope: "key:AKIATEST", Writer: testAccessKey, BucketChannelID: e.peer.ChannelID,
		BucketName: bucketName, ObjectKey: key, State: jobrepo.DeletePrepared, TargetPutMsgID: target,
		CreatedAt: e.clock.Now().UnixMilli(), UpdatedAt: e.clock.Now().UnixMilli(),
	}
	if err := e.jobs.CreateDeleteJob(e.ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if state == jobrepo.DeletePrepared {
		return
	}
	if err := e.jobs.TransitionDeleteJob(e.ctx, job.JobID, jobrepo.DeletePrepared, state, jobrepo.DeletePatch{DelHeader: &header}, job.UpdatedAt); err != nil {
		t.Fatalf("transition job: %v", err)
	}
}

func (e *env) crashAfterAttempt(t *testing.T, key string, target int64) {
	t.Helper()
	jobID := e.jobID()
	header, err := formatDel(jobID, key, target, testAccessKey, e.clock.Now().Unix())
	if err != nil {
		t.Fatalf("format del: %v", err)
	}
	job := jobrepo.DeleteJob{
		JobID: jobID, Origin: jobrepo.OriginS3, Scope: "key:AKIATEST", Writer: testAccessKey, BucketChannelID: e.peer.ChannelID,
		BucketName: bucketName, ObjectKey: key, State: jobrepo.DeletePrepared, TargetPutMsgID: target,
		CreatedAt: e.clock.Now().UnixMilli(), UpdatedAt: e.clock.Now().UnixMilli(),
	}
	if err := e.jobs.CreateDeleteJob(e.ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := e.jobs.TransitionDeleteJob(e.ctx, jobID, jobrepo.DeletePrepared, jobrepo.DeleteDelAttempted, jobrepo.DeletePatch{DelHeader: &header}, job.UpdatedAt); err != nil {
		t.Fatalf("transition job: %v", err)
	}
}

func TestResumeVersionGuard(t *testing.T) {
	cases := []struct {
		name  string
		state jobrepo.DeleteState
	}{
		{"prepared", jobrepo.DeletePrepared},
		{"del_attempted", jobrepo.DeleteDelAttempted},
		{"uncertain_del", jobrepo.DeleteUncertainDel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			old := e.seedObject("k")
			e.insertGuardJob(t, "k", old.PutMsgID, tc.state)
			newer := e.seedObject("k")
			sends := e.client.CallCount(faketg.MethodSendControlIdempotent)
			job := e.jobs.only(t)
			if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if got := e.jobs.only(t); got.State != jobrepo.DeleteAborted {
				t.Fatalf("job state = %s, want aborted", got.State)
			}
			if e.client.CallCount(faketg.MethodSendControlIdempotent) != sends || e.client.CallCount(faketg.MethodDeleteMessages) != 0 {
				t.Fatalf("a stale job sent a tombstone or deleted messages")
			}
			if e.liveObjects() != 1 || !e.channelHas(newer.PutMsgID) {
				t.Fatalf("the newer version must survive a stale delete job")
			}
		})
	}
}

func (e *env) insertGuardJob(t *testing.T, key string, target int64, state jobrepo.DeleteState) {
	t.Helper()
	switch state {
	case jobrepo.DeletePrepared:
		e.insertJob(t, key, target, state, "")
	default:
		header, err := formatDel(e.jobID(), key, target, testAccessKey, e.clock.Now().Unix())
		if err != nil {
			t.Fatalf("format del: %v", err)
		}
		e.insertJob(t, key, target, jobrepo.DeleteDelAttempted, header)
		if state == jobrepo.DeleteUncertainDel {
			job := e.jobs.only(t)
			if err := e.jobs.TransitionDeleteJob(e.ctx, job.JobID, jobrepo.DeleteDelAttempted, state, jobrepo.DeletePatch{}, job.UpdatedAt); err != nil {
				t.Fatalf("transition job: %v", err)
			}
		}
	}
}

func TestResumeTerminalJobIsNoop(t *testing.T) {
	e := newEnv(t)
	e.seedObject("k")
	if err := e.svc.Delete(e.ctx, bucketName, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	job := e.jobs.only(t)
	sends := e.client.CallCount(faketg.MethodSendControlIdempotent)
	if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if e.client.CallCount(faketg.MethodSendControlIdempotent) != sends {
		t.Fatalf("resume of a committed job sent a message")
	}
	if err := e.svc.Resume(e.ctx, "00000000-0000-4000-8000-999999999999"); !errors.Is(err, jobrepo.ErrJobNotFound) {
		t.Fatalf("Resume unknown job error = %v", err)
	}
}

func TestFloodWaitOnTombstoneIsSlowDown(t *testing.T) {
	e := newEnv(t)
	e.seedObject("k")
	if err := e.client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 40e9}); err != nil {
		t.Fatalf("queue fault: %v", err)
	}
	err := e.svc.Delete(e.ctx, bucketName, "k")
	if !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("Delete error = %v, want SlowDown", err)
	}
	if e.liveObjects() != 1 || e.client.CallCount(faketg.MethodDeleteMessages) != 0 {
		t.Fatalf("flood wait must leave the object untouched")
	}
	if err := e.svc.Resume(e.ctx, e.jobs.only(t).JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if e.liveObjects() != 0 {
		t.Fatalf("resume did not finish the delete")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	base := newEnv(t).deps(nil)
	mutations := map[string]func(d *Dependencies){
		"buckets":          func(d *Dependencies) { d.Buckets = nil },
		"gates":            func(d *Dependencies) { d.Gates = nil },
		"authority":        func(d *Dependencies) { d.Authority = nil },
		"key locks":        func(d *Dependencies) { d.KeyLocks = nil },
		"objects":          func(d *Dependencies) { d.Objects = nil },
		"operations":       func(d *Dependencies) { d.Operations = nil },
		"jobs":             func(d *Dependencies) { d.Jobs = nil },
		"sender":           func(d *Dependencies) { d.Sender = nil },
		"messages":         func(d *Dependencies) { d.Messages = nil },
		"peers":            func(d *Dependencies) { d.Peers = nil },
		"projector":        func(d *Dependencies) { d.Projector = nil },
		"writer":           func(d *Dependencies) { d.Writer = nil },
		"drainer":          func(d *Dependencies) { d.Drainer = nil },
		"principals":       func(d *Dependencies) { d.Principals = nil },
		"stable random id": func(d *Dependencies) { d.StableRandomID = nil },
		"clock":            func(d *Dependencies) { d.Clock = nil },
		"random":           func(d *Dependencies) { d.Random = nil },
		"logger":           func(d *Dependencies) { d.Logger = nil },
		"actor":            func(d *Dependencies) { d.ActorUserID = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			deps := base
			deps.Drainer = &Drainer{}
			mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := New(withDrainer(base)); err != nil {
		t.Fatalf("New with all dependencies: %v", err)
	}
}

func withDrainer(d Dependencies) Dependencies {
	d.Drainer = &Drainer{}
	return d
}

func TestJobIDIsUUIDv4(t *testing.T) {
	id, err := newJobID(strings.NewReader(strings.Repeat("\xff", 16)))
	if err != nil {
		t.Fatalf("newJobID: %v", err)
	}
	if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("job id %q is not a UUIDv4", id)
	}
	if _, err := newJobID(strings.NewReader("short")); err == nil {
		t.Fatalf("newJobID accepted a short random source")
	}
}

func TestTimeUnits(t *testing.T) {
	e := newEnv(t)
	e.seedObject("k")
	if err := e.svc.Delete(e.ctx, bucketName, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	nowMilli := e.clock.Now().UnixMilli()
	job := e.jobs.only(t)
	if job.CreatedAt != nowMilli || job.UpdatedAt != nowMilli {
		t.Fatalf("job times = %d/%d, want milliseconds %d", job.CreatedAt, job.UpdatedAt, nowMilli)
	}
	if got := e.count(`SELECT COUNT(*) FROM deleted_messages WHERE source = 'cleanup' AND recorded_at = ?1`, nowMilli); got != 2 {
		t.Fatalf("deleted_messages recorded_at in milliseconds = %d rows, want 2", got)
	}
	if !strings.Contains(job.DelHeader, "ts="+strconv.FormatInt(e.clock.Now().Unix(), 10)) {
		t.Fatalf("op ts must stay in seconds: %s", job.DelHeader)
	}
}

func TestTombstoneProjectedWithTelegramDate(t *testing.T) {
	e := newEnv(t)
	e.seedObject("k")
	e.reader.shift = 777
	if err := e.svc.Delete(e.ctx, bucketName, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	job := e.jobs.only(t)
	want := e.clock.Now().Unix() + 777
	if got := e.count(`SELECT msg_date FROM replay_log WHERE channel_id = ?1 AND msg_id = ?2`, e.peer.ChannelID, job.DelMsgID); int64(got) != want {
		t.Fatalf("replay_log msg_date = %d, want Telegram date %d", got, want)
	}
	if got := e.count(`SELECT msg_date FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'k'`, e.peer.ChannelID); int64(got) != want {
		t.Fatalf("tombstone msg_date = %d, want %d", got, want)
	}
}

func TestReadBackFailureLeavesJobAttempted(t *testing.T) {
	e := newEnv(t)
	seed := e.seedObject("k")
	e.reader.fail = errors.New("history unavailable")
	err := e.svc.Delete(e.ctx, bucketName, "k")
	if !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("Delete error = %v, want SlowDown", err)
	}
	job := e.jobs.only(t)
	if job.State != jobrepo.DeleteDelAttempted {
		t.Fatalf("job state = %s, want del_attempted", job.State)
	}
	if e.liveObjects() != 1 || e.client.CallCount(faketg.MethodDeleteMessages) != 0 || !e.channelHas(seed.PutMsgID) {
		t.Fatalf("nothing may be projected or deleted without the sent message")
	}
	if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1 AND msg_id > ?2`, e.peer.ChannelID, seed.PutMsgID); got != 0 {
		t.Fatalf("tombstone projected with a local-clock date")
	}
	e.reader.fail = nil
	if err := e.svc.Resume(e.ctx, job.JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if e.jobs.only(t).State != jobrepo.DeleteCommitted || e.liveObjects() != 0 {
		t.Fatalf("resume did not finish the delete")
	}
	tombstones := 0
	for _, sent := range e.net.SentControls(e.peer.ChannelID) {
		if strings.Contains(sent.Text, "t=del") {
			tombstones++
		}
	}
	if tombstones != 1 {
		t.Fatalf("tombstones = %d, want 1", tombstones)
	}
}
