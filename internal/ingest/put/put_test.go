package put

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func mustPut(t *testing.T, w *world, req object.PutRequest, body []byte) object.ObjectMeta {
	t.Helper()
	meta, err := w.svc.Put(w.ctx, req, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put(%q): %v", req.Key, err)
	}
	return meta
}

func TestPutAcknowledgedAfterProjection(t *testing.T) {
	w := newWorld(t, "none")
	body := []byte("acknowledged only after the commit is projected")
	var projectedAtSend bool
	w.sender.ctlHook = func(_ int, _ tgclient.SendControlRequest) (int64, error, bool) {
		_, projectedAtSend = w.liveObject("a.txt")
		return 0, nil, false
	}

	meta := mustPut(t, w, w.putRequest("a.txt", len(body)), body)

	if projectedAtSend {
		t.Fatal("the object was visible before the t=put was sent")
	}
	live, ok := w.liveObject("a.txt")
	if !ok || live.PutMsgID != meta.PutMsgID || meta.PutMsgID <= 0 {
		t.Fatalf("live = %+v ok=%v, returned = %+v: the response must follow the projection", live, ok, meta)
	}
	controls := w.sentControls()
	files := w.sentFiles()
	if len(controls) != 1 || len(files) != 1 || controls[0].MsgID != meta.PutMsgID {
		t.Fatalf("controls=%+v files=%+v", controls, files)
	}
	op, err := wire.Parse(wire.ExtractHeaderLine(controls[0].Text))
	if err != nil {
		t.Fatalf("put header: %v", err)
	}
	if op.Type != wire.OpPut || op.BodyMsgID != files[0].MsgID || op.StoredSize != int64(len(body)) || op.Writer != "key."+testAccessKeyID {
		t.Fatalf("put op = %+v", op)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadCommitted || job.PutMsgID != meta.PutMsgID || job.Scope != "key:"+testAccessKeyID || job.Origin != jobrepo.OriginS3 {
		t.Fatalf("job = %+v", job)
	}
	if w.spool.exists(job.JobID) {
		t.Fatal("spool file left behind after commit")
	}
	if w.admission.released != 1 || w.gate.released != 1 {
		t.Fatalf("slot released %d, gate released %d, want both 1", w.admission.released, w.gate.released)
	}
	if sum := md5.Sum(body); meta.ETag != object.SingleETag(sum) {
		t.Fatalf("etag = %q, want the body md5", meta.ETag)
	}
}

func TestPutProjectedMessageDateIsTelegramDate(t *testing.T) {
	w := newWorld(t, "none")
	skewed := clock.NewFake(time.Unix(1763000000+7200, 0))
	deps := w.deps
	deps.Clock = skewed
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("dated by telegram")
	meta, err := svc.Put(w.ctx, w.putRequest("dated.txt", len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	const telegramDate = int64(1763000000)
	if meta.MsgDate != telegramDate {
		t.Fatalf("returned msg date = %d, want the telegram date %d", meta.MsgDate, telegramDate)
	}
	var stored int64
	err = w.db.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT msg_date FROM objects WHERE channel_id = ?1 AND object_key = 'dated.txt'`, testChannelID).Scan(&stored)
	})
	if err != nil || stored != telegramDate || stored == skewed.Now().Unix() {
		t.Fatalf("objects.msg_date = %d err=%v, want %d and not the local clock", stored, err, telegramDate)
	}
}

func TestPutFailsWithoutLocalClockFallbackWhenReadBackFails(t *testing.T) {
	w := newWorld(t, "none")
	deps := w.deps
	deps.Messages = failingMessages{}
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("read back fails")
	_, err = svc.Put(w.ctx, w.putRequest("rb.txt", len(body)), bytes.NewReader(body))
	if err == nil {
		t.Fatal("Put succeeded without a message date")
	}
	if _, ok := w.liveObject("rb.txt"); ok {
		t.Fatal("an object was projected with a guessed date")
	}
}

type failingMessages struct{}

func (failingMessages) GetMessages(context.Context, tgclient.InputPeer, []int64) ([]tgclient.HistoryMessage, error) {
	return nil, errors.New("history unavailable")
}

func TestPutScopeAndWriterRule(t *testing.T) {
	tests := []struct {
		name       string
		caller     string
		wantScope  string
		wantWriter string
		wantOrigin jobrepo.Origin
	}{
		{name: "s3 access key", caller: testAccessKeyID, wantScope: "key:" + testAccessKeyID, wantWriter: "key." + testAccessKeyID, wantOrigin: jobrepo.OriginS3},
		{name: "admin device", caller: "device:7QXK2M", wantScope: "device:7QXK2M", wantWriter: "dev.7QXK2M", wantOrigin: jobrepo.OriginAdmin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, "none")
			req := w.putRequest("who.txt", 3)
			req.AccessKeyID = tc.caller
			mustPut(t, w, req, []byte("abc"))
			job := w.jobs.only()
			if job.Scope != tc.wantScope || job.Writer != tc.wantWriter || job.Origin != tc.wantOrigin {
				t.Fatalf("job scope=%q writer=%q origin=%q", job.Scope, job.Writer, job.Origin)
			}
			if len(w.admission.acquired) != 1 || w.admission.acquired[0] != tc.wantScope {
				t.Fatalf("admission scope = %v, want %q", w.admission.acquired, tc.wantScope)
			}
		})
	}
}

func TestPutZeroByteObjectSkipsBody(t *testing.T) {
	w := newWorld(t, "none")
	meta := mustPut(t, w, w.putRequest("empty", 0), nil)
	if len(w.sentFiles()) != 0 || len(w.sentControls()) != 1 {
		t.Fatalf("files=%d controls=%d, want no body and one put", len(w.sentFiles()), len(w.sentControls()))
	}
	live, ok := w.liveObject("empty")
	if !ok || live.BodyMsgID != 0 || live.StoredSize != 0 || live.ETag != wire.EmptyMD5 || meta.PutMsgID != live.PutMsgID {
		t.Fatalf("live = %+v ok=%v", live, ok)
	}
}

func TestPutRejectsBeforeAnySpoolOrSend(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*world)
		mutate  func(*object.PutRequest)
		wantErr error
	}{
		{name: "no such bucket", mutate: func(r *object.PutRequest) { r.Bucket = "missing" }, wantErr: errs.ErrNoSuchBucket},
		{name: "no write authority", setup: func(w *world) { w.authority.denyWrite = errs.ErrServiceUnavailable }, wantErr: errs.ErrServiceUnavailable},
		{name: "admission refuses", setup: func(w *world) { w.admission.refuseAcquire = &slowDown{} }, wantErr: errs.ErrSlowDown},
		{name: "object too large", mutate: func(r *object.PutRequest) { r.ContentLength = 1992294401 }, wantErr: errs.ErrObjectTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, "none")
			if tc.setup != nil {
				tc.setup(w)
			}
			req := w.putRequest("k", 4)
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			_, err := w.svc.Put(w.ctx, req, strings.NewReader("data"))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if w.spool.writes != 0 || w.jobs.count() != 0 || len(w.sender.files) != 0 {
				t.Fatalf("spooled=%d jobs=%d sends=%d, want nothing", w.spool.writes, w.jobs.count(), len(w.sender.files))
			}
		})
	}
}

func TestSpoolHashMismatchFailsJob(t *testing.T) {
	w := newWorld(t, "none")
	_, job := w.prepare("tampered.bin", []byte("original bytes"), nil)
	if err := os.WriteFile(job.SpoolPath, []byte("changed bytes!"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := w.svc.Resume(w.ctx, job.JobID)
	if !errors.Is(err, errs.ErrBadDigest) {
		t.Fatalf("Resume error = %v, want ErrBadDigest", err)
	}
	stored, _ := w.jobs.GetJob(w.ctx, job.JobID)
	if stored.State != jobrepo.UploadAborted || stored.ErrorCode != errs.CodeBadDigest {
		t.Fatalf("job = %s code %q, want aborted with bad digest", stored.State, stored.ErrorCode)
	}
	if len(w.sender.files) != 0 || w.spool.exists(job.JobID) {
		t.Fatal("a tampered spool file reached Telegram or was kept")
	}
}

func TestOverwriteQueuesSupersededCleanup(t *testing.T) {
	w := newWorld(t, "none")
	first := mustPut(t, w, w.putRequest("same.txt", 3), []byte("one"))
	if len(w.cleanup.drained) != 0 {
		t.Fatal("a first write must not drain cleanup")
	}
	second := mustPut(t, w, w.putRequest("same.txt", 3), []byte("two"))
	if second.PutMsgID <= first.PutMsgID {
		t.Fatalf("versions %d then %d", first.PutMsgID, second.PutMsgID)
	}
	rows := w.countRows(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1 AND reason = 'superseded' AND msg_id IN (?2, ?3)`,
		testChannelID, first.PutMsgID, first.BodyMsgID)
	if rows != 2 {
		t.Fatalf("queued superseded rows = %d, want the old put and body", rows)
	}
	if got := w.countRows(`SELECT COUNT(*) FROM pending_message_cleanup WHERE msg_id IN (?1, ?2)`, second.PutMsgID, second.BodyMsgID); got != 0 {
		t.Fatalf("the live version was queued for cleanup: %d rows", got)
	}
	if len(w.cleanup.drained) != 1 || w.cleanup.drained[0] != testChannelID {
		t.Fatalf("drained = %v, want one drain of the bucket channel", w.cleanup.drained)
	}
}

func TestCleanupDrainFailureNeverFailsPut(t *testing.T) {
	w := newWorld(t, "none")
	mustPut(t, w, w.putRequest("k", 1), []byte("a"))
	w.cleanup.err = errors.New("drain failed")
	mustPut(t, w, w.putRequest("k", 1), []byte("b"))
}

func TestIfNoneMatchAtCommit(t *testing.T) {
	t.Run("existing key fails early", func(t *testing.T) {
		w := newWorld(t, "none")
		mustPut(t, w, w.putRequest("k", 1), []byte("a"))
		sends := len(w.sender.files)
		req := w.putRequest("k", 1)
		req.IfNoneMatchAny = true
		if _, err := w.svc.Put(w.ctx, req, strings.NewReader("b")); !errors.Is(err, errs.ErrPreconditionFailed) {
			t.Fatalf("error = %v", err)
		}
		if len(w.sender.files) != sends {
			t.Fatal("an obvious precondition failure still uploaded")
		}
	})
	t.Run("racing writer wins before commit", func(t *testing.T) {
		w := newWorld(t, "none")
		w.sender.fileHook = func(call int, _ tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
			if call == 1 {
				mustPut(t, w, w.putRequest("race", 6), []byte("winner"))
			}
			return tgclient.SendFileResult{}, nil, false
		}
		req := w.putRequest("race", 5)
		req.IfNoneMatchAny = true
		_, err := w.svc.Put(w.ctx, req, strings.NewReader("loser"))
		if !errors.Is(err, errs.ErrPreconditionFailed) {
			t.Fatalf("error = %v, want precondition failed at commit", err)
		}
		files := w.sentFiles()
		if len(files) != 2 {
			t.Fatalf("sent files = %d, want the winner and the loser body", len(files))
		}
		if got := w.deleted(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != files[1].MsgID {
			t.Fatalf("deleted = %v, want exactly the loser body %d", got, files[1].MsgID)
		}
		if len(w.sentControls()) != 1 {
			t.Fatalf("controls = %d, the failed put must never be sent", len(w.sentControls()))
		}
		var failed jobrepo.UploadJob
		for _, job := range w.jobs.jobs {
			if job.ErrorCode == errs.CodePreconditionFailed {
				failed = job
			}
		}
		if failed.State != jobrepo.UploadAborted {
			t.Fatalf("losing job = %+v, want aborted", failed)
		}
	})
}

func TestIfMatchChecks(t *testing.T) {
	w := newWorld(t, "none")
	first := mustPut(t, w, w.putRequest("m", 1), []byte("a"))
	tests := []struct {
		name    string
		ifMatch string
		wantErr error
	}{
		{name: "matching quoted etag", ifMatch: `"` + first.ETag + `"`},
		{name: "star", ifMatch: "*"},
		{name: "other etag", ifMatch: strings.Repeat("0", 32), wantErr: errs.ErrPreconditionFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := w.putRequest("m", 1)
			req.IfMatch = tc.ifMatch
			_, err := w.svc.Put(w.ctx, req, strings.NewReader("a"))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	req := w.putRequest("absent", 1)
	req.IfMatch = "*"
	if _, err := w.svc.Put(w.ctx, req, strings.NewReader("a")); !errors.Is(err, errs.ErrPreconditionFailed) {
		t.Fatalf("if-match on a missing key = %v", err)
	}
}

func TestFloodWaitPastBudgetSlowDown(t *testing.T) {
	w := newWorld(t, "none")
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent,
		faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.Put(w.ctx, w.putRequest("slow", 3), strings.NewReader("abc"))
	if !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("error = %v, want SlowDown", err)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadAborted || job.ErrorCode != errs.CodeSlowDown {
		t.Fatalf("job = %s code %q, want aborted with slow down", job.State, job.ErrorCode)
	}
	if len(w.sentFiles()) != 0 || len(w.sentControls()) != 0 || w.spool.exists(job.JobID) {
		t.Fatal("a refused flood wait left Telegram or spool state behind")
	}
	if len(w.admission.floods) != 1 || w.admission.floods[0] != 10*time.Minute {
		t.Fatalf("flood waits = %v", w.admission.floods)
	}
}

func TestFloodWaitWithinBudgetIsWaitedOut(t *testing.T) {
	w := newWorld(t, "none")
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent,
		faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, w, w.putRequest("patient", 3), []byte("abc"))
	if len(w.admission.floods) != 1 || len(w.sentFiles()) != 1 {
		t.Fatalf("floods=%v files=%d", w.admission.floods, len(w.sentFiles()))
	}
}

func TestPutFloodWaitPastBudgetBeforePutSendMarksJobUncertain(t *testing.T) {
	w := newWorld(t, "none")
	w.sender.ctlHook = func(call int, _ tgclient.SendControlRequest) (int64, error, bool) {
		if call == 1 {
			return 0, &tgclient.FloodWaitError{Duration: 10 * time.Minute}, true
		}
		return 0, nil, false
	}
	_, err := w.svc.Put(w.ctx, w.putRequest("slowput", 3), strings.NewReader("abc"))
	if !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("error = %v, want SlowDown", err)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadUncertainPut || job.PutHeader == "" {
		t.Fatalf("job = %s header %q: after put_attempted the job is uncertain, never aborted", job.State, job.PutHeader)
	}
	if len(w.deleted()) != 0 {
		t.Fatal("the body of an attempted put must never be deleted")
	}
	if err := w.svc.Resume(w.ctx, job.JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(w.sender.controls) != 2 || w.sender.controls[0] != w.sender.controls[1] {
		t.Fatalf("control random ids = %v, want the same id reused", w.sender.controls)
	}
	done := w.jobs.only()
	if done.State != jobrepo.UploadCommitted {
		t.Fatalf("resumed job = %s", done.State)
	}
	controls := w.sentControls()
	if len(controls) != 1 || controls[0].Text != job.PutHeader {
		t.Fatalf("controls = %+v, want the stored header sent once", controls)
	}
}

func TestIdempotentReplayReturnsSameObject(t *testing.T) {
	w := newWorld(t, "none")
	req := w.putRequest("idem.txt", 6)
	req.IdempotencyKey = "upload-1"
	first := mustPut(t, w, req, []byte("sixsix"))
	sends := len(w.sender.files) + len(w.sender.controls)

	second := mustPut(t, w, req, []byte("sixsix"))
	if second.PutMsgID != first.PutMsgID || second.ETag != first.ETag || second.JobID != first.JobID {
		t.Fatalf("replay = %+v, want %+v", second, first)
	}
	if len(w.sender.files)+len(w.sender.controls) != sends || w.jobs.count() != 1 {
		t.Fatal("a replayed upload sent again or created a job")
	}

	if _, err := w.svc.Put(w.ctx, req, strings.NewReader("other!")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different body error = %v, want conflict", err)
	}
	other := req
	other.AccessKeyID = "BDBKOTHERCALLER00000"
	third := mustPut(t, w, other, []byte("sixsix"))
	if third.JobID == first.JobID {
		t.Fatal("another access key shared an idempotency record")
	}
}

func TestIdempotentReplayOfOpenJobReportsUnknownOutcome(t *testing.T) {
	w := newWorld(t, "none")
	req := w.putRequest("open.txt", 3)
	req.IdempotencyKey = "k1"
	w.jobs.failNext[jobrepo.UploadPutAttempted] = errors.New("crash before put")
	if _, err := w.svc.Put(w.ctx, req, strings.NewReader("abc")); err == nil {
		t.Fatal("first Put should fail")
	}
	_, err := w.svc.Put(w.ctx, req, strings.NewReader("abc"))
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.JobID == "" {
		t.Fatalf("error = %v, want an outcome-unknown error naming the job", err)
	}
}

func TestSingleUploadRecoversAfterTransientFailure(t *testing.T) {
	w := newWorld(t, "none")
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent, faketg.Fault{Kind: faketg.FaultOutcomeUnknown}); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello transient retry")
	meta := mustPut(t, w, w.putRequest("note.txt", len(body)), body)
	if meta.StoredSize != int64(len(body)) || len(w.sender.files) != 2 {
		t.Fatalf("meta=%+v attempts=%d", meta, len(w.sender.files))
	}
}

func TestUploadFailsWhenTransientBudgetExhausted(t *testing.T) {
	w := newWorld(t, "none")
	lost := faketg.Fault{Kind: faketg.FaultOutcomeUnknown}
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent, lost, lost, lost, lost); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.Put(w.ctx, w.putRequest("movie.bin", 4), strings.NewReader("data"))
	if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
		t.Fatalf("error = %v, want an unknown outcome, never success", err)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadUncertainBody {
		t.Fatalf("job = %s, want uncertain_body", job.State)
	}
	if _, ok := w.liveObject("movie.bin"); ok {
		t.Fatal("an object exists without a commit")
	}
}

func TestSingleUploadRetriesLostAcceptedReceiptIdempotently(t *testing.T) {
	w := newWorld(t, "none")
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent,
		faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	meta := mustPut(t, w, w.putRequest("receipt-lost.txt", 17), []byte("one accepted body"))
	if len(w.sender.files) != 2 {
		t.Fatalf("send attempts = %d, want two", len(w.sender.files))
	}
	if w.sender.files[0].randomID <= 0 || w.sender.files[0].randomID != w.sender.files[1].randomID {
		t.Fatalf("random ids = %+v, want the same positive id", w.sender.files)
	}
	sent := w.sentFiles()
	if len(sent) != 1 || meta.BodyMsgID != sent[0].MsgID {
		t.Fatalf("sent = %+v, body msg %d, want exactly the original receipt", sent, meta.BodyMsgID)
	}
}

func TestEmitAndProjectContextRetriesControlWithStableRandomID(t *testing.T) {
	type ctxKey struct{}
	w := newWorld(t, "none")
	w.sender.ctlHook = func(call int, _ tgclient.SendControlRequest) (int64, error, bool) {
		if call == 1 {
			return 0, &tgclient.FloodWaitError{Duration: 0}, true
		}
		return 0, nil, false
	}
	ctx := context.WithValue(context.Background(), ctxKey{}, "request context")
	meta, err := w.svc.Put(ctx, w.putRequest("ctx.txt", 3), strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(w.sender.controls) != 2 || w.sender.controls[0] <= 0 || w.sender.controls[0] != w.sender.controls[1] {
		t.Fatalf("control random ids = %v, want one stable positive id", w.sender.controls)
	}
	want, _ := stableRandomID(meta.JobID, "put")
	if w.sender.controls[0] != want {
		t.Fatalf("random id %d is not derived from the job id (%d)", w.sender.controls[0], want)
	}
	for attempt, got := range w.sender.controlCtx {
		if got.Value(ctxKey{}) != "request context" {
			t.Fatalf("attempt %d lost the request context", attempt+1)
		}
	}
}

func TestEmitAndProjectBatchMarksPostSendProjectionFailure(t *testing.T) {
	w := newWorld(t, "none")
	err := w.db.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_put_projection BEFORE INSERT ON objects BEGIN SELECT RAISE(ABORT, 'injected projection failure'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Put(w.ctx, w.putRequest("p.txt", 3), strings.NewReader("abc"))
	var projectionErr *ControlProjectionError
	if !errors.Is(err, ErrControlProjection) || !errors.As(err, &projectionErr) || projectionErr.MsgID <= 0 {
		t.Fatalf("error = %v, want ErrControlProjection with the accepted message id", err)
	}
	if !strings.Contains(err.Error(), "injected projection failure") {
		t.Fatalf("error = %v, want the root cause", err)
	}
	controls := w.sentControls()
	if len(controls) != 1 || controls[0].MsgID != projectionErr.MsgID {
		t.Fatalf("controls = %+v, want one accepted control", controls)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadUncertainPut || job.PutMsgID != projectionErr.MsgID {
		t.Fatalf("job = %s put msg %d, want uncertain_put recording the accepted message", job.State, job.PutMsgID)
	}
	err = w.db.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TRIGGER fail_put_projection`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Resume(w.ctx, job.JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(w.sender.controls) != 1 {
		t.Fatalf("control sends = %d, an accepted put is never resent", len(w.sender.controls))
	}
	if live, ok := w.liveObject("p.txt"); !ok || live.PutMsgID != projectionErr.MsgID {
		t.Fatalf("live = %+v ok=%v", live, ok)
	}
}

func TestResumeBodySentUnderVersionGuard(t *testing.T) {
	crash := func(w *world) jobrepo.UploadJob {
		w.jobs.failNext[jobrepo.UploadPutAttempted] = errors.New("crash before put_attempted")
		if _, err := w.svc.Put(w.ctx, w.putRequest("guard", 5), strings.NewReader("first")); err == nil {
			t.Fatal("Put survived the injected crash")
		}
		job := w.jobs.only()
		if job.State != jobrepo.UploadBodySent {
			t.Fatalf("job = %s, want body_sent", job.State)
		}
		return job
	}
	t.Run("key unchanged commits", func(t *testing.T) {
		w := newWorld(t, "none")
		job := crash(w)
		if err := w.svc.Resume(w.ctx, job.JobID); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if live, ok := w.liveObject("guard"); !ok || live.JobID != job.JobID {
			t.Fatalf("live = %+v ok=%v", live, ok)
		}
	})
	t.Run("newer write aborts and keeps the body for the sweeper", func(t *testing.T) {
		w := newWorld(t, "none")
		job := crash(w)
		newer := w.putRequest("guard", 5)
		w.jobs.failNext = map[jobrepo.UploadState]error{}
		other, err := w.svc.Put(w.ctx, newer, strings.NewReader("newer"))
		if err != nil {
			t.Fatal(err)
		}
		err = w.svc.Resume(w.ctx, job.JobID)
		if !errors.Is(err, errs.ErrPreconditionFailed) {
			t.Fatalf("Resume error = %v, want the version guard", err)
		}
		stored, _ := w.jobs.GetJob(w.ctx, job.JobID)
		if stored.State != jobrepo.UploadAborted {
			t.Fatalf("job = %s, want aborted", stored.State)
		}
		if live, _ := w.liveObject("guard"); live.PutMsgID != other.PutMsgID {
			t.Fatal("a stale resumed job replaced a newer write")
		}
		if len(w.deleted()) != 0 {
			t.Fatalf("a resumed job deleted messages itself: %v", w.deleted())
		}
	})
}

func TestDiscardHiddenReceiptRejectsMessageIDsWithoutProjectedOwnership(t *testing.T) {
	w := newWorld(t, "none")
	live := mustPut(t, w, w.putRequest("owned.bin", 5), []byte("owned"))
	stranger := jobrepo.UploadJob{
		JobID: "11111111-1111-4111-8111-111111111111", BucketChannelID: testChannelID,
		ObjectKey: "owned.bin", BodyMsgID: live.BodyMsgID,
	}
	w.svc.discardBody(w.ctx, stranger)
	if len(w.deleted()) != 0 {
		t.Fatalf("a message owned by a live object reached Telegram delete: %+v", w.deleted())
	}
	stranger.BodyMsgID = 0
	w.svc.discardBody(w.ctx, stranger)
	if len(w.deleted()) != 0 {
		t.Fatalf("an empty receipt reached Telegram delete: %+v", w.deleted())
	}
}

func TestPutMultipartStoresGivenETag(t *testing.T) {
	w := newWorld(t, "none")
	etag := strings.Repeat("ab", 16) + "-3"
	jobID := "22222222-2222-4222-8222-222222222222"
	req := w.putRequest("big.bin", 4)

	meta, err := w.svc.PutMultipart(w.ctx, req, strings.NewReader("abcd"), "upload0123456789abcdef0123456789", jobID, etag)
	if err != nil {
		t.Fatalf("PutMultipart: %v", err)
	}
	job := w.jobs.only()
	if job.Kind != jobrepo.KindMultipart || job.MultipartUploadID != "upload0123456789abcdef0123456789" || job.JobID != jobID || job.ETag != etag {
		t.Fatalf("job = %+v", job)
	}
	live, ok := w.liveObject("big.bin")
	if !ok || live.ETag != etag || meta.ETag != etag || live.JobID != jobID {
		t.Fatalf("live = %+v ok=%v", live, ok)
	}
	sends := len(w.sender.files) + len(w.sender.controls)
	again, err := w.svc.PutMultipart(w.ctx, req, strings.NewReader("abcd"), "upload0123456789abcdef0123456789", jobID, etag)
	if err != nil || again.PutMsgID != meta.PutMsgID || len(w.sender.files)+len(w.sender.controls) != sends {
		t.Fatalf("retried complete = %+v, %v; want the recorded result without sending", again, err)
	}
	if _, err := w.svc.PutMultipart(w.ctx, req, strings.NewReader("abcd"), "u", jobID, strings.Repeat("ab", 16)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("a non multipart etag error = %v", err)
	}
}

func TestPutBudgetDeadlineComesFromInjectedClock(t *testing.T) {
	w := newWorld(t, "none")
	var seen admission.Budget
	deps := w.deps
	deps.Admission = &budgetSpy{inner: w.admission, seen: &seen}
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(w.ctx, w.putRequest("b", 1), strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	if want := w.clock.Now().Add(30 * time.Second); !seen.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", seen.Deadline, want)
	}
}

type budgetSpy struct {
	inner *fakeAdmission
	seen  *admission.Budget
}

func (b *budgetSpy) AcquirePut(ctx context.Context, budget admission.Budget, scope string) (func(), error) {
	*b.seen = budget
	return b.inner.AcquirePut(ctx, budget, scope)
}

func (b *budgetSpy) WaitSend(ctx context.Context, budget admission.Budget, class flow.SendClass) error {
	return b.inner.WaitSend(ctx, budget, class)
}

func (b *budgetSpy) WaitFlood(ctx context.Context, budget admission.Budget, wait time.Duration) error {
	return b.inner.WaitFlood(ctx, budget, wait)
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	w := newWorld(t, "none")
	tests := map[string]func(*Dependencies){
		"spool":     func(d *Dependencies) { d.Spool = nil },
		"admission": func(d *Dependencies) { d.Admission = nil },
		"jobs":      func(d *Dependencies) { d.Jobs = nil },
		"projector": func(d *Dependencies) { d.Projector = nil },
		"messages":  func(d *Dependencies) { d.Messages = nil },
		"clock":     func(d *Dependencies) { d.Clock = nil },
		"key locks": func(d *Dependencies) { d.KeyLocks = nil },
		"logger":    func(d *Dependencies) { d.Logger = nil },
		"budget":    func(d *Dependencies) { d.Config.RequestBudget = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			deps := w.deps
			mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestPutRecordsAreMillisecondsAndOpTimestampIsSeconds(t *testing.T) {
	w := newWorld(t, "none")
	req := w.putRequest("units.txt", 3)
	req.IdempotencyKey = "units-1"
	meta := mustPut(t, w, req, []byte("abc"))
	job := w.jobs.only()
	nowMilli := w.clock.Now().UnixMilli()
	if job.CreatedAt != nowMilli || job.UpdatedAt != nowMilli {
		t.Fatalf("job created_at=%d updated_at=%d, want unix milliseconds %d", job.CreatedAt, job.UpdatedAt, nowMilli)
	}
	record, found, err := w.jobs.LookupIdempotency(w.ctx, job.Scope, "units-1")
	if err != nil || !found {
		t.Fatalf("idempotency record: found=%v err=%v", found, err)
	}
	if record.CreatedAt != nowMilli || record.ExpiresAt != nowMilli+(24*time.Hour).Milliseconds() {
		t.Fatalf("idempotency created_at=%d expires_at=%d, want milliseconds with a 24h ttl", record.CreatedAt, record.ExpiresAt)
	}
	op, err := wire.Parse(wire.ExtractHeaderLine(w.sentControls()[0].Text))
	if err != nil || op.Timestamp != w.clock.Now().Unix() {
		t.Fatalf("put op ts = %d err=%v, want unix seconds %d", op.Timestamp, err, w.clock.Now().Unix())
	}
	if meta.PutMsgID <= 0 {
		t.Fatal("put not committed")
	}
	w.clock.Advance(24*time.Hour - time.Second)
	if _, err := w.svc.Put(w.ctx, req, strings.NewReader("abc")); err != nil {
		t.Fatalf("replay inside the ttl: %v", err)
	}
	if w.jobs.count() != 1 {
		t.Fatal("a replay inside the ttl created a job")
	}
	w.clock.Advance(2 * time.Second)
	again, err := w.svc.Put(w.ctx, req, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("put after the ttl: %v", err)
	}
	if again.JobID == meta.JobID {
		t.Fatal("an expired idempotency record was still honoured")
	}
}
