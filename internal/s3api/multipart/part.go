package multipart

import (
	"fmt"
	"io"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const checksumHeaderPrefix = "X-Amz-Checksum-"

func (h *Handlers) UploadPart(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a, access.ActionWrite); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	number, err := parsePartNumber(r.URL.Query().Get(partNumberQuery))
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	record, err := h.openUpload(r, rt)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	size, wantMD5, err := h.checkPartRequest(r, a, record, number)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.stageBody(w, r, rt, a, stagingJob{record: record, number: number, size: size, wantMD5: wantMD5})
}

type stagingJob struct {
	record  UploadRecord
	number  int
	size    int64
	wantMD5 []byte
}

func (h *Handlers) openUpload(r *http.Request, rt router.Route) (UploadRecord, error) {
	uploadID := r.URL.Query().Get(uploadIDQuery)
	if uploadID == "" {
		return UploadRecord{}, fmt.Errorf("multipart: upload id is required: %w", errs.ErrNoSuchUpload)
	}
	record, err := h.lookupUpload(r.Context(), rt, uploadID)
	if err != nil {
		return UploadRecord{}, err
	}
	if err := requireOpenUpload(record); err != nil {
		return UploadRecord{}, err
	}
	return record, nil
}

func (h *Handlers) checkPartRequest(r *http.Request, a sigv4.Auth, record UploadRecord, number int) (int64, []byte, error) {
	size, err := declaredSize(r, a)
	if err != nil {
		return 0, nil, err
	}
	wantMD5, err := parseContentMD5(r.Header)
	if err != nil {
		return 0, nil, err
	}
	if err := h.checkRunningTotal(r.Context(), record, number, size); err != nil {
		return 0, nil, err
	}
	return size, wantMD5, nil
}

func (h *Handlers) stageBody(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, job stagingJob) {
	payload, err := h.deps.Verifier.Body(r, a)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	defer payload.Close()
	var body io.Reader = payload
	if job.wantMD5 != nil {
		body = newMD5VerifyingReader(body, job.wantMD5)
	}
	body = newIdleDeadlineBody(body, w, h.deps.Clock, h.deps.Config.BodyIdleTimeout)
	part, err := h.deps.Stage.PutPart(r.Context(), job.record.ID, job.number, job.size, body)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	header := w.Header()
	header.Set("ETag", object.QuoteETag(part.ETag))
	if algorithm, value := payload.Checksum(); algorithm != "" {
		header.Set(checksumHeaderPrefix+algorithm, value)
	}
	if job.record.Encrypted {
		header.Set(serverSideEncryption, serverSideEncryptionAES)
	}
	header.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}
