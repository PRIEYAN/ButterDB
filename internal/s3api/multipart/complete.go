package multipart

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const completeBodyLimit = 4 << 20

type completion struct {
	record     UploadRecord
	parts      []multipartstage.CompletedPart
	conditions multipartstage.Conditions
}

func (h *Handlers) CompleteMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	job, err := h.prepareCompletion(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if job.record.Encrypted {
		w.Header().Set(serverSideEncryption, serverSideEncryptionAES)
	}
	if job.record.State == stateCompleted {
		h.replayCompleted(w, r, rt, job)
		return
	}
	h.runCompletion(w, r, rt, job)
}

func (h *Handlers) prepareCompletion(r *http.Request, rt router.Route, a sigv4.Auth) (completion, error) {
	if _, err := h.authorize(r, rt, a, access.ActionWrite); err != nil {
		return completion{}, err
	}
	record, err := h.lookupCompletable(r, rt)
	if err != nil {
		return completion{}, err
	}
	if isStreamingMode(a.Mode) {
		return completion{}, s3err.New(s3err.CodeInvalidRequest, "Streaming payloads are accepted only for PutObject and UploadPart")
	}
	listed, err := h.readCompleteBody(r, a)
	if err != nil {
		return completion{}, err
	}
	conditions, err := parseWriteConditions(r.Header)
	if err != nil {
		return completion{}, err
	}
	if err := requireAscending(listed); err != nil {
		return completion{}, err
	}
	job := completion{record: record, conditions: conditions}
	if record.State == stateCompleted {
		job.parts = completedPartsOf(listed, nil)
		return job, nil
	}
	staged, err := h.listAllParts(r.Context(), record.ID)
	if err != nil {
		return completion{}, err
	}
	if err := validateListed(record, listed, staged); err != nil {
		return completion{}, err
	}
	job.parts = completedPartsOf(listed, stagedByNumber(staged))
	return job, nil
}

func (h *Handlers) lookupCompletable(r *http.Request, rt router.Route) (UploadRecord, error) {
	record, err := h.lookupRequestedUpload(r, rt)
	if err != nil {
		return UploadRecord{}, err
	}
	if record.State == stateAborted {
		return UploadRecord{}, fmt.Errorf("multipart: upload %s: %w", record.ID, errs.ErrNoSuchUpload)
	}
	return record, nil
}

func (h *Handlers) readCompleteBody(r *http.Request, a sigv4.Auth) ([]completePart, error) {
	payload, err := h.deps.Verifier.Body(r, a)
	if err != nil {
		return nil, err
	}
	defer payload.Close()
	var request completeRequest
	if err := xmlcodec.Decode(payload, completeBodyLimit, &request); err != nil {
		return nil, err
	}
	if len(request.Parts) == 0 {
		return nil, s3err.New(s3err.CodeMalformedXML, "")
	}
	return request.Parts, nil
}

func (h *Handlers) replayCompleted(w http.ResponseWriter, r *http.Request, rt router.Route, job completion) {
	meta, err := h.deps.Stage.Complete(r.Context(), job.record.ID, job.parts, job.conditions)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, h.completeResult(r, rt, meta))
}

func (h *Handlers) runCompletion(w http.ResponseWriter, r *http.Request, rt router.Route, job completion) {
	long, err := xmlcodec.StartLongResponse(w, h.deps.Config.KeepaliveInterval)
	if err != nil {
		return
	}
	defer long.Close()
	detached := context.WithoutCancel(r.Context())
	meta, err := h.deps.Stage.Complete(detached, job.record.ID, job.parts, job.conditions)
	if err != nil {
		h.failLong(w, rt, long, err)
		return
	}
	_ = long.Succeed(h.completeResult(r, rt, meta))
}

func (h *Handlers) completeResult(r *http.Request, rt router.Route, meta object.ObjectMeta) completeResult {
	objectPath := (&url.URL{Path: "/" + rt.Target.Bucket + "/" + rt.Target.Key}).EscapedPath()
	return completeResult{
		Location: h.baseURL(r) + objectPath,
		Bucket:   rt.Target.Bucket,
		Key:      rt.Target.Key,
		ETag:     object.QuoteETag(meta.ETag),
	}
}

func (h *Handlers) baseURL(r *http.Request) string {
	if base := strings.TrimRight(h.deps.Config.PublicURL, "/"); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
