package multipart

import (
	"context"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

type copyJob struct {
	stagingJob
	opened objectreader.Opened
	span   byteSpan
}

func (h *Handlers) UploadPartCopy(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
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
	source, err := parseCopySource(r.Header.Get(copySourceHeader))
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	meta, opened, err := h.openSource(r, a, source)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	defer opened.Close()
	span, err := h.checkCopyRequest(r, record, number, meta)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	job := copyJob{stagingJob: stagingJob{record: record, number: number, size: span.length}, opened: opened, span: span}
	h.runCopy(w, r, rt, job)
}

func (h *Handlers) openSource(r *http.Request, a sigv4.Auth, source copySource) (object.ObjectMeta, objectreader.Opened, error) {
	if _, err := object.ParseKey(source.key); err != nil {
		return object.ObjectMeta{}, nil, err
	}
	if _, err := h.authorizeBucket(r.Context(), a, source.bucket, access.ActionRead); err != nil {
		return object.ObjectMeta{}, nil, err
	}
	return h.deps.Sources.Open(r.Context(), source.bucket, source.key)
}

func (h *Handlers) checkCopyRequest(r *http.Request, record UploadRecord, number int, meta object.ObjectMeta) (byteSpan, error) {
	conditions := httprange.ConditionsFromCopySourceHeader(r.Header)
	if err := httprange.EvaluateCopySource(conditions, httprange.EntityFromMeta(meta), h.deps.Clock.Now()); err != nil {
		return byteSpan{}, err
	}
	span, err := parseCopyRange(r.Header.Get(copySourceRangeName), meta.PlaintextSize)
	if err != nil {
		return byteSpan{}, err
	}
	if err := h.checkRunningTotal(r.Context(), record, number, span.length); err != nil {
		return byteSpan{}, err
	}
	return span, nil
}

func (h *Handlers) runCopy(w http.ResponseWriter, r *http.Request, rt router.Route, job copyJob) {
	if job.record.Encrypted {
		w.Header().Set(serverSideEncryption, serverSideEncryptionAES)
	}
	long, err := xmlcodec.StartLongResponse(w, h.deps.Config.KeepaliveInterval)
	if err != nil {
		return
	}
	defer long.Close()
	detached := context.WithoutCancel(r.Context())
	part, err := h.deps.Stage.PutPart(detached, job.record.ID, job.number, job.size, newSpanReader(detached, job.opened, job.span))
	if err != nil {
		h.failLong(w, rt, long, err)
		return
	}
	modified := part.StagedAt
	if modified.IsZero() {
		modified = h.deps.Clock.Now()
	}
	_ = long.Succeed(copyPartResult{ETag: object.QuoteETag(part.ETag), LastModified: xmlcodec.FormatTime(modified)})
}
