package multipart

import (
	"net/http"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func abortSpec(uploadID string) requestSpec {
	return requestSpec{method: http.MethodDelete, target: "/photos/video/clip.mp4?uploadId=" + uploadID}
}

func TestAbortWhileCommitting(t *testing.T) {
	committing := openRecord()
	committing.State = stateCommitting
	fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: committing}})
	rec := serve(fx.handlers.AbortMultipartUpload, abortSpec(testUploadID), routeFor("AbortMultipartUpload"), writerAuth())
	parsed := assertErrorCode(t, rec, http.StatusConflict, "OperationAborted")
	if parsed.Message != abortBusyText {
		t.Fatalf("message = %q", parsed.Message)
	}
	if fx.stage.abortCalls != 0 {
		t.Fatalf("stage.Abort called for a committing upload")
	}
}

func TestAbortMultipartUpload(t *testing.T) {
	completed := openRecord()
	completed.State = stateCompleted
	aborted := openRecord()
	aborted.State = stateAborted
	elsewhere := openRecord()
	elsewhere.Key = "other/key"
	tests := []struct {
		name       string
		record     UploadRecord
		stageErr   error
		wantStatus int
		wantCode   string
		wantAborts int
	}{
		{name: "open upload is aborted", record: openRecord(), wantStatus: 204, wantAborts: 1},
		{name: "completed upload", record: completed, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "already aborted upload", record: aborted, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "upload of another key", record: elsewhere, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "upload turned busy after the lookup", record: openRecord(), stageErr: errs.ErrUploadBusy, wantStatus: 409, wantCode: "OperationAborted", wantAborts: 1},
		{name: "upload vanished after the lookup", record: openRecord(), stageErr: errs.ErrNoSuchUpload, wantStatus: 404, wantCode: "NoSuchUpload", wantAborts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: test.record}})
			fx.stage.abortErr = test.stageErr
			rec := serve(fx.handlers.AbortMultipartUpload, abortSpec(testUploadID), routeFor("AbortMultipartUpload"), writerAuth())
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
			} else if rec.Code != test.wantStatus || rec.Body.Len() != 0 {
				t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
			}
			if fx.stage.abortCalls != test.wantAborts {
				t.Fatalf("abort calls = %d, want %d", fx.stage.abortCalls, test.wantAborts)
			}
		})
	}
}

func TestAbortUnknownUpload(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	rec := serve(fx.handlers.AbortMultipartUpload, abortSpec("00000000000000000000000000000000"), routeFor("AbortMultipartUpload"), writerAuth())
	assertErrorCode(t, rec, http.StatusNotFound, "NoSuchUpload")
	rec = serve(fx.handlers.AbortMultipartUpload, requestSpec{method: http.MethodDelete, target: "/photos/video/clip.mp4"}, routeFor("AbortMultipartUpload"), writerAuth())
	assertErrorCode(t, rec, http.StatusNotFound, "NoSuchUpload")
}
