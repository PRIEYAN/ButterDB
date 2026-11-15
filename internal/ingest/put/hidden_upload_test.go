package put

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestUploadHiddenSingleIsNotVisibleBeforeCommit(t *testing.T) {
	w := newWorld(t, "none")
	body := []byte("hello from the object store")
	_, job := w.prepare("notes.txt", body, nil)

	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	if job.State != jobrepo.UploadBodySent || job.BodyMsgID <= 0 {
		t.Fatalf("job = %s body %d, want body_sent with a message", job.State, job.BodyMsgID)
	}
	sent := w.sentFiles()
	if len(sent) != 1 {
		t.Fatalf("sent files = %+v, want one", sent)
	}
	op, err := wire.Parse(wire.ExtractHeaderLine(sent[0].Caption))
	if err != nil {
		t.Fatalf("body caption: %v", err)
	}
	if op.Type != wire.OpBody || op.JobID != job.JobID || op.StoredSize != int64(len(body)) || op.Encrypted {
		t.Fatalf("body op = %+v", op)
	}
	if got := w.countRows(`SELECT COUNT(*) FROM object_bodies WHERE channel_id = ?1 AND job_id = ?2`, testChannelID, job.JobID); got != 1 {
		t.Fatalf("object_bodies rows = %d, want 1", got)
	}
	if got := w.countRows(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, testChannelID); got != 0 {
		t.Fatalf("objects = %d, want 0: a body never creates an object", got)
	}
	if !bytes.Equal(w.readBody(job.BodyMsgID), body) {
		t.Fatal("stored body differs from the spooled bytes")
	}
}

func TestUploadHiddenRetryUsesStableTelegramMessage(t *testing.T) {
	w := newWorld(t, "none")
	body := []byte("retry me")
	_, job := w.prepare("retry.txt", body, nil)
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent,
		faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}); err != nil {
		t.Fatal(err)
	}

	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	if len(w.sender.files) != 2 {
		t.Fatalf("send attempts = %d, want 2", len(w.sender.files))
	}
	want, _ := stableRandomID(job.JobID, "body")
	if w.sender.files[0].randomID != want || w.sender.files[1].randomID != want {
		t.Fatalf("random ids = %+v, want both %d", w.sender.files, want)
	}
	sent := w.sentFiles()
	if len(sent) != 1 || sent[0].MsgID != job.BodyMsgID {
		t.Fatalf("sent = %+v, job body %d, want the one original message", sent, job.BodyMsgID)
	}
}

func TestUploadHiddenRetriesBoundedFloodWaitAndRewinds(t *testing.T) {
	w := newWorld(t, "none")
	body := bytes.Repeat([]byte("complete body after retry "), 40)
	_, job := w.prepare("flood.txt", body, nil)
	w.sender.fileHook = func(call int, req tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
		if call > 2 {
			return tgclient.SendFileResult{}, nil, false
		}
		if _, err := io.CopyN(io.Discard, req.Body, 100); err != nil {
			t.Errorf("partial read: %v", err)
		}
		return tgclient.SendFileResult{}, &tgclient.FloodWaitError{Duration: time.Second}, true
	}

	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	if len(w.admission.floods) != 2 {
		t.Fatalf("flood waits = %v, want 2", w.admission.floods)
	}
	if !bytes.Equal(w.readBody(job.BodyMsgID), body) {
		t.Fatal("body after flood retries is not the complete spooled content")
	}
}

func TestUploadHiddenValidatesMetadataAndSourceBeforeRemoteAccess(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*object.PutRequest)
		body    []byte
		wantErr error
	}{
		{name: "negative content length", mutate: func(r *object.PutRequest) { r.ContentLength = -1 }, wantErr: ErrInvalidRequest},
		{name: "key too long", mutate: func(r *object.PutRequest) { r.Key = object.Key(strings.Repeat("k", 1025)) }, wantErr: errs.ErrInvalidKey},
		{name: "empty key", mutate: func(r *object.PutRequest) { r.Key = "" }, wantErr: errs.ErrInvalidKey},
		{name: "user metadata too large", mutate: func(r *object.PutRequest) {
			r.UserMeta = map[string]string{"big": strings.Repeat("v", 2100)}
		}, wantErr: errs.ErrMetadataTooLarge},
		{name: "user metadata name invalid", mutate: func(r *object.PutRequest) { r.UserMeta = map[string]string{"bad name": "v"} }, wantErr: ErrInvalidRequest},
		{name: "put record too long", mutate: func(r *object.PutRequest) {
			r.SystemMeta = map[string]string{"content-disposition": strings.Repeat("d", 1000), "cache-control": strings.Repeat("c", 1000), "content-language": strings.Repeat("l", 1000), "expires": strings.Repeat("e", 1000)}
			r.UserMeta = map[string]string{"a": strings.Repeat("v", 1500)}
		}, wantErr: errs.ErrMetadataTooLarge},
		{name: "caller identity missing", mutate: func(r *object.PutRequest) { r.AccessKeyID = "" }, wantErr: errs.ErrAccessDenied},
		{name: "caller identity not a writer name", mutate: func(r *object.PutRequest) { r.AccessKeyID = "bad key" }, wantErr: ErrInvalidRequest},
		{name: "idempotency key not printable", mutate: func(r *object.PutRequest) { r.IdempotencyKey = "a\x01b" }, wantErr: ErrInvalidRequest},
		{name: "unknown encryption mode", mutate: func(r *object.PutRequest) { r.Encryption = "aws:kms" }, wantErr: ErrInvalidRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, "none")
			req := w.putRequest("bad.bin", 4)
			tc.mutate(&req)
			p, err := planRequest(req)
			if err == nil {
				_, err = p.resolve(w.svc.deps.Buckets.(*fakeBuckets).info)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("plan error = %v, want %v", err, tc.wantErr)
			}
			if w.sender.resolves != 0 || len(w.sender.files) != 0 || len(w.sentFiles()) != 0 {
				t.Fatalf("remote access before validation: resolves=%d files=%d", w.sender.resolves, len(w.sender.files))
			}
		})
	}
}

func TestUploadHiddenRejectsSourceLengthMismatchBeforeRemoteAccess(t *testing.T) {
	w := newWorld(t, "none")
	for name, body := range map[string][]byte{"shorter": []byte("sho"), "longer": []byte("longer than declared")} {
		_, err := w.spool.Write(w.ctx, "00000000-0000-4000-8000-00000000000"+string(name[0]), bytes.NewReader(body), 6, limits.MaxStoredObjectBytes)
		if err == nil {
			t.Fatalf("%s source accepted", name)
		}
	}
	if w.sender.resolves != 0 || len(w.sender.files) != 0 {
		t.Fatal("remote access before the source was validated")
	}
}

func TestUploadHiddenRejectsEncryptedPlaintextAboveTDE1Capacity(t *testing.T) {
	w := newWorld(t, "none")
	for _, size := range []int64{math.MaxInt64, limits.MaxEncryptedPlaintextBytes + 1} {
		req := w.putRequest("too-large.bin", 0)
		req.ContentLength = size
		req.Encryption = "tde1"
		p, err := planRequest(req)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if _, err := p.resolve(w.svc.deps.Buckets.(*fakeBuckets).info); !errors.Is(err, errs.ErrObjectTooLarge) {
			t.Fatalf("size %d error = %v, want ErrObjectTooLarge", size, err)
		}
	}
	if w.sender.resolves != 0 || len(w.sender.files) != 0 {
		t.Fatal("remote access for an over-capacity encrypted upload")
	}
}

func TestUploadHiddenHandlesZeroExactChunkAndCeilingSizes(t *testing.T) {
	tests := []struct {
		name       string
		encrypted  bool
		plaintext  int64
		wantStored int64
		wantErr    error
	}{
		{name: "plain zero", plaintext: 0, wantStored: 0},
		{name: "plain exact chunk", plaintext: format.ChunkSize, wantStored: format.ChunkSize},
		{name: "plain ceiling", plaintext: limits.MaxStoredObjectBytes, wantStored: limits.MaxStoredObjectBytes},
		{name: "plain over ceiling", plaintext: limits.MaxStoredObjectBytes + 1, wantErr: errs.ErrObjectTooLarge},
		{name: "encrypted zero", encrypted: true, plaintext: 0, wantStored: 66},
		{name: "encrypted exact chunk", encrypted: true, plaintext: format.ChunkSize, wantStored: 65618},
		{name: "encrypted ceiling", encrypted: true, plaintext: limits.MaxEncryptedPlaintextBytes, wantStored: limits.MaxStoredObjectBytes},
		{name: "encrypted one over ceiling", encrypted: true, plaintext: limits.MaxEncryptedPlaintextBytes + 1, wantErr: errs.ErrObjectTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := storedSizeFor(tc.encrypted, tc.plaintext)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err == nil && stored != tc.wantStored {
				t.Fatalf("stored = %d, want %d", stored, tc.wantStored)
			}
		})
	}
}

func TestUploadHiddenSendsExactChunkBody(t *testing.T) {
	w := newWorld(t, "none")
	body := bytesOf(format.ChunkSize, 'x')
	_, job := w.prepare("chunk.bin", body, nil)
	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	if !bytes.Equal(w.readBody(job.BodyMsgID), body) {
		t.Fatal("exact chunk body differs")
	}
}

func TestUploadHiddenUnknownSendOutcomeReconcilesCurrentPartBeforeCleanup(t *testing.T) {
	w := newWorld(t, "none")
	body := []byte("accepted but the receipt was lost")
	_, job := w.prepare("lost.bin", body, nil)
	lost := faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent, lost, lost, lost); err != nil {
		t.Fatal(err)
	}

	err := w.svc.sendBody(w.ctx, &job, w.budget())
	if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) || !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("sendBody error = %v, want an unknown outcome", err)
	}
	if job.State != jobrepo.UploadUncertainBody {
		t.Fatalf("job state = %s, want uncertain_body", job.State)
	}
	sent := w.sentFiles()
	if len(sent) != 1 || len(w.deleted()) != 0 {
		t.Fatalf("sent=%+v deleted=%+v, want one accepted message and no cleanup", sent, w.deleted())
	}
	stored, _ := w.jobs.GetJob(w.ctx, job.JobID)
	if err := w.svc.sendBody(w.ctx, &stored, w.budget()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if stored.State != jobrepo.UploadBodySent || stored.BodyMsgID != sent[0].MsgID {
		t.Fatalf("reconciled job = %s body %d, want body_sent at %d", stored.State, stored.BodyMsgID, sent[0].MsgID)
	}
	if len(w.sentFiles()) != 1 || len(w.deleted()) != 0 {
		t.Fatal("reconcile duplicated or deleted the accepted message")
	}
}

func TestUploadHiddenAcceptedSendWithoutReceiptIsRecoveredBeforeCleanup(t *testing.T) {
	w := newWorld(t, "none")
	_, job := w.prepare("zero-receipt.bin", []byte("x"), nil)
	w.sender.fileHook = func(call int, req tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
		if call != 1 {
			return tgclient.SendFileResult{}, nil, false
		}
		if _, err := w.telegram.SendFileIdempotent(context.Background(), req); err != nil {
			t.Errorf("deliver: %v", err)
		}
		return tgclient.SendFileResult{}, nil, true
	}

	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	sent := w.sentFiles()
	if len(sent) != 1 || job.BodyMsgID != sent[0].MsgID || len(w.deleted()) != 0 {
		t.Fatalf("job body %d, sent %+v, deleted %+v", job.BodyMsgID, sent, w.deleted())
	}
}

func TestRecoverAndDiscardHiddenUploadPersistsRecoveredReceiptBeforeDeleteRetry(t *testing.T) {
	w := newWorld(t, "none")
	_, job := w.prepare("receipt.bin", []byte("payload"), nil)
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent,
		faketg.Fault{Kind: faketg.FaultAcceptedWithError}); err != nil {
		t.Fatal(err)
	}

	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err != nil {
		t.Fatalf("sendBody: %v", err)
	}
	stored, err := w.jobs.GetJob(w.ctx, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	sent := w.sentFiles()
	if len(sent) != 1 || stored.BodyMsgID != sent[0].MsgID || stored.State != jobrepo.UploadBodySent {
		t.Fatalf("persisted job = %s body %d, sent %+v", stored.State, stored.BodyMsgID, sent)
	}
	if len(w.sender.files) != 1 {
		t.Fatalf("send attempts = %d, an accepted message must never be resent", len(w.sender.files))
	}
}

func TestRecoverAndDiscardHiddenUploadClosesSendReceiptCrashGap(t *testing.T) {
	w := newWorld(t, "none")
	_, job := w.prepare("crash.bin", []byte("crash window"), nil)
	w.sender.fileHook = func(call int, req tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
		if _, err := w.telegram.SendFileIdempotent(context.Background(), req); err != nil {
			t.Errorf("deliver: %v", err)
		}
		return tgclient.SendFileResult{}, errors.New("process died"), true
	}
	if err := w.svc.sendBody(w.ctx, &job, w.budget()); err == nil {
		t.Fatal("sendBody succeeded through a lost process")
	}
	stored, _ := w.jobs.GetJob(w.ctx, job.JobID)
	if stored.State != jobrepo.UploadUncertainBody {
		t.Fatalf("state = %s, want uncertain_body so the receipt is reconciled before cleanup", stored.State)
	}
	if len(w.sentFiles()) != 1 || len(w.deleted()) != 0 {
		t.Fatalf("sent=%d deleted=%+v", len(w.sentFiles()), w.deleted())
	}
}
