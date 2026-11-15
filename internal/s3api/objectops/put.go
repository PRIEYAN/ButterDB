package objectops

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	headerContentMD5  = "Content-Md5"
	headerIfMatch     = "If-Match"
	headerIfNoneMatch = "If-None-Match"
	headerChecksum    = "X-Amz-Checksum-"
)

type preparedPut struct {
	request object.PutRequest
	md5     []byte
}

func (h *Handlers) PutObject(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, a, rt.Target.Bucket, access.ActionWrite)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	prepared, err := h.preparePut(r, rt, a, info)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	payload, err := h.deps.Payloads.Body(r, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	defer payload.Close()
	body := h.withIdleDeadline(w, r, newBodyCloser(newMD5VerifyingBody(payload, prepared.md5), payload))
	meta, err := h.deps.Writer.Put(commitContext(r), prepared.request, body)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	h.writePutResponse(w, meta, payload)
}

func (h *Handlers) preparePut(r *http.Request, rt router.Route, a sigv4.Auth, info bucket.Info) (preparedPut, error) {
	if err := checkWriteFeatures(r.Header, r.URL.Query()); err != nil {
		return preparedPut{}, err
	}
	if err := checkCannedACL(r.Header, info); err != nil {
		return preparedPut{}, err
	}
	if err := validateKey(rt.Target.Key); err != nil {
		return preparedPut{}, err
	}
	if a.DecodedLength < 0 {
		return preparedPut{}, s3err.New(s3err.CodeMissingContentLength, "")
	}
	conditions, err := parseWriteConditions(r.Header)
	if err != nil {
		return preparedPut{}, err
	}
	attrs, err := parseAttributes(r.Header)
	if err != nil {
		return preparedPut{}, err
	}
	sum, err := parseContentMD5(r.Header.Get(headerContentMD5))
	if err != nil {
		return preparedPut{}, err
	}
	requested, err := requestedEncryption(r.Header)
	if err != nil {
		return preparedPut{}, err
	}
	if err := checkStoredSize(a.DecodedLength, resolveEncrypted(requested, info)); err != nil {
		return preparedPut{}, err
	}
	accessKeyID := string(a.Key.ID)
	if err := checkPutRecord(rt.Target.Key, attrs.contentType, attrs.userMeta, attrs.systemMeta, accessKeyID); err != nil {
		return preparedPut{}, err
	}
	request := object.PutRequest{
		Bucket:         rt.Target.Bucket,
		Key:            object.Key(rt.Target.Key),
		ContentLength:  a.DecodedLength,
		ContentType:    attrs.contentType,
		UserMeta:       attrs.userMeta,
		SystemMeta:     attrs.systemMeta,
		Encryption:     requested,
		AccessKeyID:    accessKeyID,
		IfMatch:        conditions.ifMatch,
		IfNoneMatchAny: conditions.ifNoneMatchAny,
	}
	return preparedPut{request: request, md5: sum}, nil
}

type writeConditions struct {
	ifMatch        string
	ifNoneMatchAny bool
}

func parseWriteConditions(header http.Header) (writeConditions, error) {
	var conditions writeConditions
	if value := strings.TrimSpace(header.Get(headerIfNoneMatch)); value != "" {
		if value != "*" {
			return writeConditions{}, invalidArgument("If-None-Match is supported only with the value *")
		}
		conditions.ifNoneMatchAny = true
	}
	conditions.ifMatch = strings.TrimSpace(header.Get(headerIfMatch))
	return conditions, nil
}

func (h *Handlers) writePutResponse(w http.ResponseWriter, meta object.ObjectMeta, payload *sigv4.PayloadReader) {
	header := w.Header()
	header.Set("ETag", object.QuoteETag(meta.ETag))
	if meta.Encrypted {
		header.Set(headerEncryption, encryptionAES256)
	}
	if algorithm, value := payload.Checksum(); algorithm != "" {
		header.Set(headerChecksum+algorithm, value)
	}
	header.Set("Content-Length", strconv.Itoa(0))
	w.WriteHeader(http.StatusOK)
}

func commitContext(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}

type bodyCloser struct {
	io.Reader
	closer io.Closer
}

func newBodyCloser(reader io.Reader, closer io.Closer) io.ReadCloser {
	return &bodyCloser{Reader: reader, closer: closer}
}

func (b *bodyCloser) Close() error {
	return b.closer.Close()
}
