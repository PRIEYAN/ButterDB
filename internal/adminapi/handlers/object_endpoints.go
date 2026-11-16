package handlers

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const (
	defaultListLimit       = 200
	maxListLimit           = 1000
	deviceScopePrefix      = "device:"
	maxIdempotencyKeyChars = 128
	sha256HexChars         = 64
	md5DigestBytes         = 16
)

func (a *api) registerObjectRoutes() {
	a.authed("GET "+adminv1.PathBucketObjects, a.listObjects)
	a.authed("POST "+adminv1.PathBucketObjectsDelete, a.deleteObjects)
	a.authed("GET "+adminv1.PathBucketObjectMeta, a.objectMeta)
	a.authedStream("GET "+adminv1.PathBucketObject, a.downloadObject)
	a.authedStream("PUT "+adminv1.PathBucketObject, a.uploadObject)
	a.authed("DELETE "+adminv1.PathBucketObject, a.deleteObject)
	a.authed("POST "+adminv1.PathBucketPresign, a.presignObject)
}

func objectKeyParam(r *http.Request) (object.Key, error) {
	values := r.URL.Query()[adminv1.QueryKey]
	if len(values) != 1 {
		return "", &BadRequestError{Field: adminv1.QueryKey}
	}
	return object.ParseKey(values[0])
}

func (a *api) listObjects(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	query, err := listQueryFromRequest(r)
	if err != nil {
		return err
	}
	page, err := a.deps.Lister.List(r.Context(), name, query)
	if err != nil {
		return err
	}
	list := adminv1.ObjectList{
		Bucket:         string(name),
		Prefix:         query.Prefix,
		Delimiter:      query.Delimiter,
		Objects:        make([]adminv1.ObjectSummary, 0, len(page.Objects)),
		CommonPrefixes: page.CommonPrefixes,
		NextCursor:     page.NextToken,
	}
	if list.CommonPrefixes == nil {
		list.CommonPrefixes = []string{}
	}
	for _, meta := range page.Objects {
		list.Objects = append(list.Objects, objectSummaryDTO(meta))
	}
	a.writeJSON(w, r, http.StatusOK, list)
	return nil
}

func listQueryFromRequest(r *http.Request) (object.ListQuery, error) {
	values := r.URL.Query()
	query := object.ListQuery{
		Prefix:            values.Get(adminv1.QueryPrefix),
		Delimiter:         values.Get(adminv1.QueryDelimiter),
		ContinuationToken: values.Get(adminv1.QueryCursor),
		Limit:             defaultListLimit,
	}
	if !utf8.ValidString(query.Prefix) {
		return object.ListQuery{}, &BadRequestError{Field: adminv1.QueryPrefix}
	}
	if utf8.RuneCountInString(query.Delimiter) > 1 || !utf8.ValidString(query.Delimiter) {
		return object.ListQuery{}, &BadRequestError{Field: adminv1.QueryDelimiter}
	}
	if raw := values.Get(adminv1.QueryLimit); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxListLimit {
			return object.ListQuery{}, &BadRequestError{Field: adminv1.QueryLimit}
		}
		query.Limit = parsed
	}
	return query, nil
}

func (a *api) objectMeta(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	key, err := objectKeyParam(r)
	if err != nil {
		return err
	}
	meta, err := a.deps.Lister.Meta(r.Context(), name, key)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, objectMetaDTO(string(name), meta))
	return nil
}

func (a *api) downloadObject(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	key, err := objectKeyParam(r)
	if err != nil {
		return err
	}
	tracked := &trackingWriter{ResponseWriter: w}
	if err := a.deps.Server.ServeObject(tracked, r, name, key); err != nil {
		if tracked.wrote {
			a.deps.Logger.WarnContext(r.Context(), "admin download failed after the response started", "request_id", RequestIDFromContext(r.Context()), "error", err)
			return nil
		}
		return err
	}
	return nil
}

func (a *api) deleteObject(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	key, err := objectKeyParam(r)
	if err != nil {
		return err
	}
	err = a.deps.Writer.Delete(r.Context(), name, key)
	if errors.Is(err, errs.ErrNoSuchKey) {
		err = nil
	}
	a.record(r, "object.delete", objectTarget(name, key), err)
	if err != nil {
		return err
	}
	a.writeNoContent(w)
	return nil
}

func objectTarget(name bucket.BucketName, key object.Key) string {
	return string(name) + "/" + string(key)
}

func (a *api) deleteObjects(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	var req adminv1.DeleteObjectsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if len(req.Keys) < 1 || len(req.Keys) > limits.MaxDeleteObjectsKeys {
		return &ValidationError{Field: "keys", Reason: "must hold 1 to 1000 keys"}
	}
	result := adminv1.DeleteObjectsResult{Deleted: make([]string, 0, len(req.Keys)), Errors: []adminv1.DeleteObjectError{}}
	for _, raw := range req.Keys {
		if err := r.Context().Err(); err != nil {
			return err
		}
		a.deleteOne(r.Context(), name, raw, &result)
	}
	a.recordBatch(r, name, result)
	a.writeJSON(w, r, http.StatusOK, result)
	return nil
}

func (a *api) deleteOne(ctx context.Context, name bucket.BucketName, raw string, result *adminv1.DeleteObjectsResult) {
	key, err := object.ParseKey(raw)
	if err == nil {
		err = a.deps.Writer.Delete(ctx, name, key)
	}
	if err == nil || errors.Is(err, errs.ErrNoSuchKey) {
		result.Deleted = append(result.Deleted, raw)
		return
	}
	mapped := MapError(err)
	result.Errors = append(result.Errors, adminv1.DeleteObjectError{
		Key:       raw,
		Code:      mapped.Body.Code,
		Message:   mapped.Body.Message,
		Retryable: mapped.Body.Retryable,
	})
}

func (a *api) recordBatch(r *http.Request, name bucket.BucketName, result adminv1.DeleteObjectsResult) {
	outcome, code := outcomeSuccess, ""
	switch {
	case len(result.Errors) > 0 && len(result.Deleted) == 0:
		outcome, code = outcomeFailure, string(result.Errors[0].Code)
	case len(result.Errors) > 0:
		outcome, code = outcomePartial, string(result.Errors[0].Code)
	}
	a.writeAudit(r, a.actor(r.Context()), "object.delete_batch", string(name), outcome, code)
}

func (a *api) uploadObject(w http.ResponseWriter, r *http.Request) error {
	device, err := a.currentDevice(r)
	if err != nil {
		return err
	}
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	key, err := objectKeyParam(r)
	if err != nil {
		return err
	}
	if r.ContentLength < 0 {
		return failure(adminv1.CodeLengthRequired, nil)
	}
	if r.ContentLength > limits.MaxStoredObjectBytes {
		return errs.ErrObjectTooLarge
	}
	req, md5Want, sha256Want, err := putRequestFromHeaders(r, name, key)
	if err != nil {
		return err
	}
	req.AccessKeyID = deviceScopePrefix + device.DeviceID
	ctx, cancel := a.commitContext(r.Context())
	defer cancel()
	body := newDigestVerifyingBody(&abandonOnReadError{source: r.Body, cancel: cancel}, md5Want, sha256Want)
	meta, err := a.deps.Writer.Put(ctx, req, body)
	a.record(r, "object.upload", objectTarget(name, key), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, objectMetaDTO(string(name), meta))
	return nil
}

func (a *api) commitContext(parent context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(parent)
	if budget := a.deps.Config.UploadCommitBudget; budget > 0 {
		return context.WithTimeout(detached, budget)
	}
	return context.WithCancel(detached)
}

type abandonOnReadError struct {
	source io.Reader
	cancel context.CancelFunc
}

func (b *abandonOnReadError) Read(p []byte) (int, error) {
	n, err := b.source.Read(p)
	if err != nil && err != io.EOF {
		b.cancel()
	}
	return n, err
}

func putRequestFromHeaders(r *http.Request, name bucket.BucketName, key object.Key) (object.PutRequest, []byte, []byte, error) {
	header := r.Header
	req := object.PutRequest{
		Bucket:        string(name),
		Key:           key,
		ContentLength: r.ContentLength,
		ContentType:   header.Get("Content-Type"),
		Encryption:    header.Get(adminv1.HeaderEncryption),
		IfMatch:       strings.TrimSpace(header.Get("If-Match")),
	}
	if len(req.ContentType) > limits.MaxContentTypeBytes {
		return req, nil, nil, &ValidationError{Field: "Content-Type", Reason: "too long"}
	}
	if req.Encryption != "" && req.Encryption != bucket.EncryptionNone && req.Encryption != bucket.EncryptionTDE1 {
		return req, nil, nil, &ValidationError{Field: adminv1.HeaderEncryption, Reason: "must be none or tde1"}
	}
	if value := strings.TrimSpace(header.Get("If-None-Match")); value != "" {
		if value != "*" {
			return req, nil, nil, &ValidationError{Field: "If-None-Match", Reason: "only * is supported"}
		}
		req.IfNoneMatchAny = true
	}
	if values := header.Values(adminv1.HeaderIdempotencyKey); len(values) > 0 {
		if len(values) != 1 || !validIdempotencyKey(values[0]) {
			return req, nil, nil, &ValidationError{Field: adminv1.HeaderIdempotencyKey, Reason: "must be 1 to 128 printable ASCII characters"}
		}
		req.IdempotencyKey = values[0]
	}
	userMeta, err := userMetaFromHeaders(header)
	if err != nil {
		return req, nil, nil, err
	}
	req.UserMeta = userMeta
	md5Want, sha256Want, err := checksumsFromHeaders(header)
	return req, md5Want, sha256Want, err
}

func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > maxIdempotencyKeyChars {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func userMetaFromHeaders(header http.Header) (map[string]string, error) {
	meta := map[string]string{}
	prefix := strings.ToLower(adminv1.HeaderMetaPrefix)
	for rawName, values := range header {
		name, ok := strings.CutPrefix(strings.ToLower(rawName), prefix)
		if !ok {
			continue
		}
		if name == "" {
			return nil, &BadRequestError{Field: adminv1.HeaderMetaPrefix}
		}
		meta[object.NormalizeUserMetaName(name)] = strings.Join(values, ",")
	}
	if len(meta) == 0 {
		return nil, nil
	}
	if err := object.ValidateUserMeta(meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func checksumsFromHeaders(header http.Header) ([]byte, []byte, error) {
	var md5Want, sha256Want []byte
	if value := strings.TrimSpace(header.Get("Content-MD5")); value != "" {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != md5DigestBytes {
			return nil, nil, &BadRequestError{Field: "Content-MD5"}
		}
		md5Want = decoded
	}
	if value := strings.TrimSpace(header.Get(adminv1.HeaderContentSHA256)); value != "" {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(value) != sha256HexChars {
			return nil, nil, &BadRequestError{Field: adminv1.HeaderContentSHA256}
		}
		sha256Want = decoded
	}
	return md5Want, sha256Want, nil
}
