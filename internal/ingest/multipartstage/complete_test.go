package multipartstage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const minPart = limits.MinMultipartPartBytes

func listOf(parts ...Part) []CompletedPart {
	listed := make([]CompletedPart, 0, len(parts))
	for _, part := range parts {
		listed = append(listed, CompletedPart{Number: part.Number, ETag: `"` + part.ETag + `"`})
	}
	return listed
}

func stageTwoParts(t *testing.T, h *harness, key string) (Upload, Part, Part, []byte, []byte) {
	t.Helper()
	upload := h.create(t, key, false)
	first, second := filled(minPart, 'a'), []byte("tail bytes")
	return upload, h.putPart(t, upload.ID, 1, first), h.putPart(t, upload.ID, 2, second), first, second
}

func TestCompleteConcatenatesListedPartsOnly(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "video/clip.mp4", false)
	first, skipped, last := filled(minPart, 'a'), filled(1234, 'b'), []byte("final part")
	p1 := h.putPart(t, upload.ID, 1, first)
	h.putPart(t, upload.ID, 2, skipped)
	p3 := h.putPart(t, upload.ID, 3, last)

	meta, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p3), Conditions{IfMatch: `"abc"`, IfNoneMatchAny: true})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if h.putter.callCount() != 1 {
		t.Fatalf("put calls = %d, want 1", h.putter.callCount())
	}
	call := h.putter.calls[0]
	if !bytes.Equal(call.body, append(append([]byte{}, first...), last...)) {
		t.Fatalf("body is not part 1 followed by part 3 (%d bytes)", len(call.body))
	}
	req := call.req
	if req.ContentLength != int64(len(call.body)) || req.Bucket != "photos" || req.Key != "video/clip.mp4" || req.ContentType != "video/mp4" ||
		req.UserMeta["owner"] != "alice" || req.SystemMeta["cache-control"] != "no-cache" || req.AccessKeyID != "AKIATEST" ||
		req.IfMatch != `"abc"` || !req.IfNoneMatchAny || req.Encryption != "none" {
		t.Fatalf("put request = %+v", req)
	}
	if call.uploadID != upload.ID || len(call.jobID) != 36 || meta.JobID != call.jobID {
		t.Fatalf("upload id %q job id %q meta job %q", call.uploadID, call.jobID, meta.JobID)
	}
	if _, err := os.Stat(h.stage.uploadDir(upload.ID)); !os.IsNotExist(err) {
		t.Fatalf("staging directory still present: %v", err)
	}
}

func TestEncryptedCompleteAsksForEncryption(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "enc", true)
	part := h.putPart(t, upload.ID, 1, []byte("secret"))
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(part), Conditions{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := h.putter.calls[0].req.Encryption; got != "tde1" {
		t.Fatalf("encryption = %q, want tde1", got)
	}
}

func TestCompleteMultipartETag(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, first, second := stageTwoParts(t, h, "k")
	sum1, sum2 := md5.Sum(first), md5.Sum(second)
	joined := md5.Sum(append(sum1[:], sum2[:]...))
	want := hex.EncodeToString(joined[:]) + "-2"

	meta, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if meta.ETag != want {
		t.Fatalf("etag = %s, want %s", meta.ETag, want)
	}
	if got := h.putter.calls[0].etag; got != want {
		t.Fatalf("etag passed to put = %s, want %s", got, want)
	}
	row, err := h.real.GetUpload(context.Background(), upload.ID)
	if err != nil || row.ETag != want {
		t.Fatalf("stored etag = %q err=%v", row.ETag, err)
	}
}

func TestCompleteStateTransitions(t *testing.T) {
	flood := &tgclient.FloodWaitError{Duration: 30 * time.Second}
	tests := []struct {
		name      string
		putErr    error
		wantState jobrepo.MultipartState
		wantIs    []error
	}{
		{name: "committed", wantState: jobrepo.MultipartCompleted},
		{name: "flood wait goes back to open", putErr: flood, wantState: jobrepo.MultipartOpen, wantIs: []error{errs.ErrSlowDown, tgclient.ErrFloodWait}},
		{name: "slow down goes back to open", putErr: fmt.Errorf("put: %w", errs.ErrSlowDown), wantState: jobrepo.MultipartOpen, wantIs: []error{errs.ErrSlowDown}},
		{name: "precondition failure goes back to open", putErr: errs.ErrPreconditionFailed, wantState: jobrepo.MultipartOpen, wantIs: []error{errs.ErrPreconditionFailed}},
		{name: "internal failure goes back to open", putErr: errors.New("disk exploded"), wantState: jobrepo.MultipartOpen},
		{name: "unknown send stays committing", putErr: tgclient.ErrSendOutcomeUnknown, wantState: jobrepo.MultipartCommitting, wantIs: []error{errs.ErrSlowDown, tgclient.ErrSendOutcomeUnknown}},
		{name: "canceled put stays committing", putErr: context.Canceled, wantState: jobrepo.MultipartCommitting, wantIs: []error{context.Canceled}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
			h.putter.setErr(tc.putErr)
			_, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{})
			if (tc.putErr == nil) != (err == nil) {
				t.Fatalf("err = %v for put error %v", err, tc.putErr)
			}
			for _, target := range tc.wantIs {
				if !errors.Is(err, target) {
					t.Fatalf("err = %v, want it to match %v", err, target)
				}
			}
			row, getErr := h.real.GetUpload(context.Background(), upload.ID)
			if getErr != nil || row.State != tc.wantState {
				t.Fatalf("state = %q err=%v, want %q", row.State, getErr, tc.wantState)
			}
			switch tc.wantState {
			case jobrepo.MultipartOpen:
				if row.JobID != "" {
					t.Fatalf("job id %q kept after reopening", row.JobID)
				}
				if _, statErr := os.Stat(h.partFile(upload.ID, 1)); statErr != nil {
					t.Fatalf("parts must be kept for a retry: %v", statErr)
				}
			case jobrepo.MultipartCommitting:
				if len(row.JobID) != 36 || row.JobID != h.putter.calls[0].jobID {
					t.Fatalf("committing job id %q, put got %q", row.JobID, h.putter.calls[0].jobID)
				}
				if _, statErr := os.Stat(h.partFile(upload.ID, 1)); statErr != nil {
					t.Fatalf("parts must be kept while committing: %v", statErr)
				}
			case jobrepo.MultipartCompleted:
				if row.PutMsgID != 9001 || len(row.PartListSHA256) != 64 || row.CompletedAt != testStart.UnixMilli() || row.JobID != h.putter.calls[0].jobID {
					t.Fatalf("completed row = %+v", row)
				}
			}
		})
	}
}

func TestCompleteReopenedUploadCanBeRetried(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	h.putter.setErr(errs.ErrPreconditionFailed)
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); !errors.Is(err, errs.ErrPreconditionFailed) {
		t.Fatalf("first err = %v", err)
	}
	h.putter.setErr(nil)
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if first, second := h.putter.calls[0].jobID, h.putter.calls[1].jobID; first == second {
		t.Fatalf("a retried commit after reopening must mint a new job id, both were %s", first)
	}
}

func TestCompleteReplayIdenticalPartList(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	ctx := context.Background()
	first, err := h.stage.Complete(ctx, upload.ID, listOf(p1, p2), Conditions{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	replay, err := h.stage.Complete(ctx, upload.ID, []CompletedPart{{Number: 1, ETag: p1.ETag}, {Number: 2, ETag: `"` + p2.ETag + `"`}}, Conditions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ETag != first.ETag || replay.PutMsgID != first.PutMsgID || replay.JobID != first.JobID {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	if h.putter.callCount() != 1 {
		t.Fatalf("put calls = %d, want 1", h.putter.callCount())
	}
	if _, err := h.stage.Complete(ctx, upload.ID, listOf(p1), Conditions{}); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("different list err = %v, want no such upload", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 3, 1, bytes.NewReader([]byte("x"))); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("part after completion err = %v, want no such upload", err)
	}
}

func TestCompleteReplayInCommittingReusesJobID(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	ctx := context.Background()
	h.putter.setErr(tgclient.ErrSendOutcomeUnknown)
	if _, err := h.stage.Complete(ctx, upload.ID, listOf(p1, p2), Conditions{}); !errors.Is(err, errs.ErrSlowDown) {
		t.Fatalf("first err = %v, want slow down", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 1, 1, bytes.NewReader([]byte("x"))); !errors.Is(err, errs.ErrUploadBusy) {
		t.Fatalf("part while committing err = %v, want upload busy", err)
	}
	h.putter.setErr(nil)
	meta, err := h.stage.Complete(ctx, upload.ID, listOf(p1, p2), Conditions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if h.putter.callCount() != 2 || h.putter.calls[0].jobID != h.putter.calls[1].jobID || meta.JobID != h.putter.calls[0].jobID {
		t.Fatalf("job ids = %q then %q", h.putter.calls[0].jobID, h.putter.calls[1].jobID)
	}
	row, _ := h.real.GetUpload(ctx, upload.ID)
	if row.State != jobrepo.MultipartCompleted {
		t.Fatalf("state = %s, want completed", row.State)
	}
}

func TestCompleteConcurrentCallsShareOneJob(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	h.putter.started = make(chan struct{}, 4)
	h.putter.release = make(chan struct{})
	ctx := context.Background()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	run := func() {
		defer wg.Done()
		_, err := h.stage.Complete(ctx, upload.ID, listOf(p1, p2), Conditions{})
		results <- err
	}
	wg.Add(1)
	go run()
	<-h.putter.started
	wg.Add(1)
	go run()
	if _, err := h.stage.Complete(ctx, upload.ID, listOf(p1), Conditions{}); !errors.Is(err, errs.ErrUploadBusy) {
		t.Fatalf("different list while committing err = %v, want upload busy", err)
	}
	h.putter.release <- struct{}{}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	if h.putter.callCount() != 1 {
		t.Fatalf("put calls = %d, want 1", h.putter.callCount())
	}
}

func TestCompleteValidation(t *testing.T) {
	type staged struct{ p1, p2, small, last Part }
	tests := []struct {
		name    string
		listed  func(s staged) []CompletedPart
		wantErr error
	}{
		{name: "no parts", listed: func(staged) []CompletedPart { return nil }, wantErr: errs.ErrInvalidPart},
		{name: "unstaged part", listed: func(s staged) []CompletedPart { return []CompletedPart{{Number: 9, ETag: s.p1.ETag}} }, wantErr: errs.ErrInvalidPart},
		{name: "wrong etag", listed: func(staged) []CompletedPart {
			return []CompletedPart{{Number: 1, ETag: hex.EncodeToString(make([]byte, 16))}}
		}, wantErr: errs.ErrInvalidPart},
		{name: "checksum mismatch", listed: func(s staged) []CompletedPart {
			return []CompletedPart{{Number: 1, ETag: s.p1.ETag, Checksum: "u1x2Pw=="}}
		}, wantErr: errs.ErrInvalidPart},
		{name: "descending", listed: func(s staged) []CompletedPart { return listOf(s.p2, s.p1) }, wantErr: errs.ErrInvalidPartOrder},
		{name: "duplicate", listed: func(s staged) []CompletedPart { return listOf(s.p1, s.p1) }, wantErr: errs.ErrInvalidPartOrder},
		{name: "small part not last", listed: func(s staged) []CompletedPart { return listOf(s.p1, s.small, s.last) }, wantErr: errs.ErrEntityTooSmall},
		{name: "small first of two", listed: func(s staged) []CompletedPart { return listOf(s.small, s.last) }, wantErr: errs.ErrEntityTooSmall},
		{name: "small last part is fine", listed: func(s staged) []CompletedPart { return listOf(s.p1, s.small) }},
		{name: "etag case and quotes are ignored", listed: func(s staged) []CompletedPart {
			return []CompletedPart{{Number: 1, ETag: `"` + upperHex(s.p1.ETag) + `"`}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
			s := staged{p1: p1, p2: p2, small: h.putPart(t, upload.ID, 3, []byte("small")), last: h.putPart(t, upload.ID, 4, []byte("last"))}
			_, err := h.stage.Complete(context.Background(), upload.ID, tc.listed(s), Conditions{})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if h.putter.callCount() != 0 {
				t.Fatal("put ran for an invalid part list")
			}
			row, _ := h.real.GetUpload(context.Background(), upload.ID)
			if row.State != jobrepo.MultipartOpen {
				t.Fatalf("state = %s after validation failure, want open", row.State)
			}
		})
	}
}

func upperHex(s string) string {
	return string(bytes.ToUpper([]byte(s)))
}

func TestCompleteRejectsTruncatedStagedFile(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	if err := os.Truncate(h.partFile(upload.ID, 2), 3); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); !errors.Is(err, errs.ErrInvalidPart) {
		t.Fatalf("err = %v, want invalid part", err)
	}
}

func TestCompleteRejectsListedTotalOverCeiling(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	seedPartRow(t, h, upload.ID, 1, limits.MaxStoredObjectBytes/2+1)
	seedPartRow(t, h, upload.ID, 2, limits.MaxStoredObjectBytes/2+1)
	zero := hex.EncodeToString(make([]byte, 16))
	listed := []CompletedPart{{Number: 1, ETag: zero}, {Number: 2, ETag: zero}}
	if _, err := h.stage.Complete(context.Background(), upload.ID, listed, Conditions{}); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("err = %v, want object too large", err)
	}
}

func TestAbortRemovesFiles(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	h.putPart(t, upload.ID, 1, []byte("one"))
	h.putPart(t, upload.ID, 2, []byte("two"))
	if err := h.stage.Abort(context.Background(), upload.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, err := os.Stat(h.stage.uploadDir(upload.ID)); !os.IsNotExist(err) {
		t.Fatalf("directory still present: %v", err)
	}
	if _, err := h.real.GetUpload(context.Background(), upload.ID); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("row still present: %v", err)
	}
	if err := h.stage.Abort(context.Background(), upload.ID); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("second abort err = %v, want no such upload", err)
	}
}

func TestAbortCommittingRejected(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	h.putter.setErr(tgclient.ErrSendOutcomeUnknown)
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); err == nil {
		t.Fatal("complete unexpectedly succeeded")
	}
	if err := h.stage.Abort(context.Background(), upload.ID); !errors.Is(err, errs.ErrUploadBusy) {
		t.Fatalf("abort err = %v, want upload busy", err)
	}
	if _, err := os.Stat(h.partFile(upload.ID, 1)); err != nil {
		t.Fatalf("part files must survive a rejected abort: %v", err)
	}
}

func TestAbortCompletedIsGone(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := h.stage.Abort(context.Background(), upload.ID); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("abort err = %v, want no such upload", err)
	}
}

func TestSweepIdleSkipsCommitting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	idle := h.create(t, "idle", false)
	h.putPart(t, idle.ID, 1, []byte("idle bytes"))
	committing, p1, p2, _, _ := stageTwoParts(t, h, "committing")
	h.putter.setErr(tgclient.ErrSendOutcomeUnknown)
	if _, err := h.stage.Complete(ctx, committing.ID, listOf(p1, p2), Conditions{}); err == nil {
		t.Fatal("complete unexpectedly succeeded")
	}
	h.clock.Advance(8 * 24 * time.Hour)
	fresh := h.create(t, "fresh", false)

	aborted, err := h.stage.SweepIdle(ctx, 7*24*time.Hour)
	if err != nil || aborted != 1 {
		t.Fatalf("aborted = %d err=%v, want 1", aborted, err)
	}
	if _, err := h.real.GetUpload(ctx, idle.ID); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("idle upload still present: %v", err)
	}
	if _, err := os.Stat(h.stage.uploadDir(idle.ID)); !os.IsNotExist(err) {
		t.Fatalf("idle files still present: %v", err)
	}
	row, err := h.real.GetUpload(ctx, committing.ID)
	if err != nil || row.State != jobrepo.MultipartCommitting {
		t.Fatalf("committing row = %+v err=%v", row, err)
	}
	if _, err := os.Stat(h.partFile(committing.ID, 1)); err != nil {
		t.Fatalf("committing files touched: %v", err)
	}
	if _, err := h.real.GetUpload(ctx, fresh.ID); err != nil {
		t.Fatalf("fresh upload swept: %v", err)
	}
}

type staleIndex struct {
	Index
	ids []string
}

func (s staleIndex) IdleBefore(context.Context, time.Time, int) ([]string, error) {
	return s.ids, nil
}

func TestSweepIdleIgnoresCommittingReturnedByIndex(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	committing, p1, p2, _, _ := stageTwoParts(t, h, "committing")
	h.putter.setErr(tgclient.ErrSendOutcomeUnknown)
	if _, err := h.stage.Complete(ctx, committing.ID, listOf(p1, p2), Conditions{}); err == nil {
		t.Fatal("complete unexpectedly succeeded")
	}
	h.stage.deps.Index = staleIndex{Index: h.index, ids: []string{committing.ID, "../escape"}}
	aborted, err := h.stage.SweepIdle(ctx, time.Hour)
	if err != nil || aborted != 0 {
		t.Fatalf("aborted = %d err=%v, want 0", aborted, err)
	}
	if _, err := os.Stat(h.partFile(committing.ID, 1)); err != nil {
		t.Fatalf("committing files touched: %v", err)
	}
}

func TestNoTransactionDuringPut(t *testing.T) {
	h := newHarness(t)
	upload, p1, p2, _, _ := stageTwoParts(t, h, "k")
	if _, err := h.stage.Complete(context.Background(), upload.ID, listOf(p1, p2), Conditions{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if call := h.putter.calls[0]; call.busy {
		t.Fatal("an index call was in flight while Put ran")
	}
}

func TestNoTransactionWhilePartStreams(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	probe := &activityProbe{index: h.index, data: []byte("streamed")}
	if _, err := h.stage.PutPart(context.Background(), upload.ID, 1, int64(len(probe.data)), probe); err != nil {
		t.Fatalf("put part: %v", err)
	}
	if probe.busyDuringRead {
		t.Fatal("an index call was in flight while the part body streamed")
	}
	if !probe.read {
		t.Fatal("probe body never read")
	}
}

type activityProbe struct {
	index          *guardedIndex
	data           []byte
	pos            int
	read           bool
	busyDuringRead bool
}

func (p *activityProbe) Read(b []byte) (int, error) {
	p.read = true
	if p.index.active.Load() != 0 {
		p.busyDuringRead = true
	}
	if p.pos >= len(p.data) {
		return 0, io.EOF
	}
	n := copy(b, p.data[p.pos:])
	p.pos += n
	return n, nil
}
