package objectops

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	headerCopySource   = "X-Amz-Copy-Source"
	headerMetadataDir  = "X-Amz-Metadata-Directive"
	headerTaggingDir   = "X-Amz-Tagging-Directive"
	selfCopyText       = "This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes."
	copyReadChunkBytes = 256 * 1024
)

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

type preparedCopy struct {
	source    copySource
	meta      object.ObjectMeta
	opened    objectreader.Opened
	request   object.PutRequest
	encrypted bool
}

func (h *Handlers) CopyObject(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	prepared, err := h.prepareCopy(r, rt, a)
	if err != nil {
		h.fail(w, r, failureRoute(rt, err), a, err)
		return
	}
	defer prepared.opened.Close()
	if prepared.encrypted {
		w.Header().Set(headerEncryption, encryptionAES256)
	}
	h.runCopy(w, r, rt, a, prepared)
}

func (h *Handlers) prepareCopy(r *http.Request, rt router.Route, a sigv4.Auth) (preparedCopy, error) {
	info, err := h.authorize(r, a, rt.Target.Bucket, access.ActionWrite)
	if err != nil {
		return preparedCopy{}, err
	}
	source, replace, err := h.checkCopyHeaders(r, a, info)
	if err != nil {
		return preparedCopy{}, err
	}
	if err := validateKey(rt.Target.Key); err != nil {
		return preparedCopy{}, err
	}
	if _, err := h.authorize(r, a, source.bucket, access.ActionRead); err != nil {
		return preparedCopy{}, &sourceFailure{source: source, err: err}
	}
	meta, opened, err := h.openSource(r, a, rt, source)
	if err != nil {
		return preparedCopy{}, err
	}
	prepared, err := h.finishCopy(r, rt, a, info, source, replace, meta, opened)
	if err != nil {
		opened.Close()
		return preparedCopy{}, err
	}
	return prepared, nil
}

func (h *Handlers) checkCopyHeaders(r *http.Request, a sigv4.Auth, info bucket.Info) (copySource, bool, error) {
	if err := checkWriteFeatures(r.Header, r.URL.Query()); err != nil {
		return copySource{}, false, err
	}
	if err := checkCannedACL(r.Header, info); err != nil {
		return copySource{}, false, err
	}
	if r.Header.Get(headerIfMatch) != "" || r.Header.Get(headerIfNoneMatch) != "" {
		return copySource{}, false, notImplemented("")
	}
	if err := checkTaggingDirective(r.Header.Get(headerTaggingDir)); err != nil {
		return copySource{}, false, err
	}
	replace, err := parseMetadataDirective(r.Header.Get(headerMetadataDir))
	if err != nil {
		return copySource{}, false, err
	}
	source, err := parseCopySource(r.Header.Get(headerCopySource))
	return source, replace, err
}

func (h *Handlers) openSource(r *http.Request, a sigv4.Auth, rt router.Route, source copySource) (object.ObjectMeta, objectreader.Opened, error) {
	meta, opened, err := h.deps.Opener.Open(commitContext(r), source.bucket, source.key)
	if err != nil {
		return object.ObjectMeta{}, nil, &sourceFailure{source: source, err: err}
	}
	entity := httprange.EntityFromMeta(meta)
	if err := httprange.EvaluateCopySource(httprange.ConditionsFromCopySourceHeader(r.Header), entity, h.deps.Clock.Now()); err != nil {
		opened.Close()
		return object.ObjectMeta{}, nil, err
	}
	return meta, opened, nil
}

type sourceFailure struct {
	source copySource
	err    error
}

func (f *sourceFailure) Error() string {
	return f.err.Error()
}

func (f *sourceFailure) Unwrap() error {
	return f.err
}

func failureRoute(rt router.Route, err error) router.Route {
	var failure *sourceFailure
	if errors.As(err, &failure) {
		rt.Target = router.Target{Bucket: failure.source.bucket, Key: failure.source.key, HasKey: true}
	}
	return rt
}

func (h *Handlers) finishCopy(r *http.Request, rt router.Route, a sigv4.Auth, info bucket.Info, source copySource, replace bool, meta object.ObjectMeta, opened objectreader.Opened) (preparedCopy, error) {
	requested, err := requestedEncryption(r.Header)
	if err != nil {
		return preparedCopy{}, err
	}
	encrypted := resolveEncrypted(requested, info)
	selfCopy := source.bucket == rt.Target.Bucket && source.key == rt.Target.Key
	if selfCopy && !replace && encrypted == meta.Encrypted {
		return preparedCopy{}, invalidRequest(selfCopyText)
	}
	attrs, err := h.destinationAttributes(r, replace, meta)
	if err != nil {
		return preparedCopy{}, err
	}
	if err := checkStoredSize(meta.PlaintextSize, encrypted); err != nil {
		return preparedCopy{}, err
	}
	accessKeyID := string(a.Key.ID)
	if err := checkPutRecord(rt.Target.Key, attrs.contentType, attrs.userMeta, attrs.systemMeta, accessKeyID); err != nil {
		return preparedCopy{}, err
	}
	request := object.PutRequest{
		Bucket:        rt.Target.Bucket,
		Key:           object.Key(rt.Target.Key),
		ContentLength: meta.PlaintextSize,
		ContentType:   attrs.contentType,
		UserMeta:      attrs.userMeta,
		SystemMeta:    attrs.systemMeta,
		Encryption:    requested,
		AccessKeyID:   accessKeyID,
	}
	return preparedCopy{source: source, meta: meta, opened: opened, request: request, encrypted: encrypted}, nil
}

func (h *Handlers) destinationAttributes(r *http.Request, replace bool, meta object.ObjectMeta) (attributes, error) {
	if replace {
		return parseAttributes(r.Header)
	}
	return attributes{
		contentType: meta.ContentType,
		userMeta:    maps.Clone(meta.UserMeta),
		systemMeta:  maps.Clone(meta.SystemMeta),
	}, nil
}

func (h *Handlers) runCopy(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, prepared preparedCopy) {
	long, startErr := xmlcodec.StartLongResponse(w, h.deps.Config.KeepAliveInterval)
	if startErr != nil {
		h.deps.Logger.Warn("s3 copy response could not start", "request_id", w.Header().Get(s3err.HeaderRequestID))
	}
	result, err := h.copyBody(r, prepared)
	if long == nil {
		return
	}
	if err != nil {
		h.finishCopyFailure(w, rt, a, long, err)
		return
	}
	if err := long.Succeed(result); err != nil {
		h.deps.Logger.Warn("s3 copy response not delivered", "request_id", w.Header().Get(s3err.HeaderRequestID))
	}
}

func (h *Handlers) copyBody(r *http.Request, prepared preparedCopy) (copyObjectResult, error) {
	ctx := commitContext(r)
	body := &openedBody{ctx: ctx, source: prepared.opened, size: prepared.meta.PlaintextSize}
	meta, err := h.deps.Writer.Put(ctx, prepared.request, body)
	if err != nil {
		return copyObjectResult{}, err
	}
	return copyObjectResult{ETag: object.QuoteETag(meta.ETag), LastModified: xmlcodec.FormatTime(time.Unix(meta.MsgDate, 0))}, nil
}

func (h *Handlers) finishCopyFailure(w http.ResponseWriter, rt router.Route, a sigv4.Auth, long *xmlcodec.LongResponse, err error) {
	h.logFailure(w, rt, a, err)
	api := s3err.Map(err)
	if api.BucketName == "" {
		api.BucketName = rt.Target.Bucket
	}
	requestID, hostID := s3err.RequestIDs(w.Header())
	if failErr := long.Fail(s3err.Encode(api, requestID, hostID)); failErr != nil {
		h.deps.Logger.Warn("s3 copy error not delivered", "request_id", requestID)
	}
}

type openedBody struct {
	ctx    context.Context
	source objectreader.Opened
	size   int64
	offset int64
}

func (b *openedBody) Read(p []byte) (int, error) {
	if b.offset >= b.size {
		return 0, io.EOF
	}
	want := min(int64(len(p)), b.size-b.offset, copyReadChunkBytes)
	n, err := b.source.ReadAt(b.ctx, p[:want], b.offset)
	b.offset += int64(n)
	if n > 0 {
		return n, nil
	}
	if err == io.EOF {
		return 0, fmt.Errorf("objectops: copy source ended early: %w", io.ErrUnexpectedEOF)
	}
	return 0, err
}
