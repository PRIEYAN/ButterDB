package adminv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) objectSpec(method, template, bucket, key string) (callSpec, error) {
	if key == "" {
		return callSpec{}, errors.New("adminv1: empty object key")
	}
	path, err := pathWith(template, ParamBucket, bucket)
	if err != nil {
		return callSpec{}, err
	}
	return callSpec{method: method, path: path, query: url.Values{QueryKey: {key}}}, nil
}

func (c *Client) ListObjects(ctx context.Context, bucket string, opts ListObjectsOptions) (ObjectList, error) {
	path, err := pathWith(PathBucketObjects, ParamBucket, bucket)
	if err != nil {
		return ObjectList{}, err
	}
	query := url.Values{}
	if opts.Prefix != "" {
		query.Set(QueryPrefix, opts.Prefix)
	}
	if opts.Delimiter != "" {
		query.Set(QueryDelimiter, opts.Delimiter)
	}
	if opts.Cursor != "" {
		query.Set(QueryCursor, opts.Cursor)
	}
	if opts.Limit > 0 {
		query.Set(QueryLimit, strconv.Itoa(opts.Limit))
	}
	return call[ObjectList](ctx, c, callSpec{method: http.MethodGet, path: path, query: query})
}

func (c *Client) HeadObject(ctx context.Context, bucket, key string) (ObjectMeta, error) {
	spec, err := c.objectSpec(http.MethodHead, PathBucketObject, bucket, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, spec, nil)
	if err != nil {
		return ObjectMeta{}, err
	}
	resp, err := c.send(req)
	if err != nil {
		return ObjectMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ObjectMeta{}, c.headerError(resp)
	}
	return metaFromHeaders(bucket, key, resp.Header, resp.ContentLength), nil
}

func (c *Client) ObjectMeta(ctx context.Context, bucket, key string) (ObjectMeta, error) {
	spec, err := c.objectSpec(http.MethodGet, PathBucketObjectMeta, bucket, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	return call[ObjectMeta](ctx, c, spec)
}

func (c *Client) GetObject(ctx context.Context, bucket, key string, opts GetObjectOptions) (ObjectStream, error) {
	spec, err := c.objectSpec(http.MethodGet, PathBucketObject, bucket, key)
	if err != nil {
		return ObjectStream{}, err
	}
	rangeHeader, err := rangeHeaderValue(opts)
	if err != nil {
		return ObjectStream{}, err
	}
	req, err := c.newRequest(ctx, spec, nil)
	if err != nil {
		return ObjectStream{}, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	if opts.IfMatch != "" {
		req.Header.Set("If-Match", opts.IfMatch)
	}
	if opts.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", opts.IfNoneMatch)
	}
	resp, err := c.send(req)
	if err != nil {
		return ObjectStream{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
	default:
		defer resp.Body.Close()
		return ObjectStream{}, c.apiError(resp)
	}
	contentRange := resp.Header.Get("Content-Range")
	size := resp.ContentLength
	if total, ok := totalFromContentRange(contentRange); ok {
		size = total
	}
	return ObjectStream{
		Body:          resp.Body,
		Status:        resp.StatusCode,
		ContentLength: resp.ContentLength,
		ContentRange:  contentRange,
		Meta:          metaFromHeaders(bucket, key, resp.Header, size),
	}, nil
}

func (c *Client) PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts PutObjectOptions) (ObjectMeta, error) {
	spec, err := c.objectSpec(http.MethodPut, PathBucketObject, bucket, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	if size < 0 {
		return ObjectMeta{}, errors.New("adminv1: put object size must not be negative")
	}
	var requestBody io.ReadCloser = http.NoBody
	if size > 0 {
		if body == nil {
			return ObjectMeta{}, errors.New("adminv1: put object body is nil")
		}
		requestBody = io.NopCloser(body)
	}
	req, err := c.newRequest(ctx, spec, requestBody)
	if err != nil {
		return ObjectMeta{}, err
	}
	req.ContentLength = size
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	applyPutHeaders(req.Header, opts)
	resp, err := c.send(req)
	if err != nil {
		return ObjectMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ObjectMeta{}, c.apiError(resp)
	}
	var meta ObjectMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBodyBytes)).Decode(&meta); err != nil {
		return ObjectMeta{}, fmt.Errorf("adminv1: decode PUT response: %w", err)
	}
	return meta, nil
}

func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	spec, err := c.objectSpec(http.MethodDelete, PathBucketObject, bucket, key)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, spec, nil)
}

func (c *Client) DeleteObjects(ctx context.Context, bucket string, req DeleteObjectsRequest) (DeleteObjectsResult, error) {
	path, err := pathWith(PathBucketObjectsDelete, ParamBucket, bucket)
	if err != nil {
		return DeleteObjectsResult{}, err
	}
	return call[DeleteObjectsResult](ctx, c, callSpec{method: http.MethodPost, path: path, body: req})
}

func (c *Client) Presign(ctx context.Context, bucket string, req PresignRequest) (PresignedURL, error) {
	path, err := pathWith(PathBucketPresign, ParamBucket, bucket)
	if err != nil {
		return PresignedURL{}, err
	}
	return call[PresignedURL](ctx, c, callSpec{method: http.MethodPost, path: path, body: req})
}

func applyPutHeaders(h http.Header, opts PutObjectOptions) {
	setIfNotEmpty(h, "Content-Type", opts.ContentType)
	setIfNotEmpty(h, HeaderEncryption, opts.Encryption)
	setIfNotEmpty(h, "Content-MD5", opts.ContentMD5)
	setIfNotEmpty(h, HeaderContentSHA256, opts.ContentSHA256)
	setIfNotEmpty(h, "If-Match", opts.IfMatch)
	setIfNotEmpty(h, HeaderIdempotencyKey, opts.IdempotencyKey)
	if opts.IfNoneMatchAny {
		h.Set("If-None-Match", "*")
	}
	for name, value := range opts.UserMetadata {
		h.Set(HeaderMetaPrefix+name, value)
	}
}

func setIfNotEmpty(h http.Header, name, value string) {
	if value != "" {
		h.Set(name, value)
	}
}

func rangeHeaderValue(opts GetObjectOptions) (string, error) {
	switch {
	case opts.RangeStart < 0 || opts.RangeEnd < 0:
		return "", errors.New("adminv1: range bounds must not be negative")
	case opts.RangeStart == 0 && opts.RangeEnd == 0:
		return "", nil
	case opts.RangeEnd == 0:
		return "bytes=" + strconv.FormatInt(opts.RangeStart, 10) + "-", nil
	case opts.RangeEnd < opts.RangeStart:
		return "", errors.New("adminv1: range end is before range start")
	default:
		return "bytes=" + strconv.FormatInt(opts.RangeStart, 10) + "-" + strconv.FormatInt(opts.RangeEnd, 10), nil
	}
}

func totalFromContentRange(value string) (int64, bool) {
	slash := strings.LastIndexByte(value, '/')
	if slash < 0 {
		return 0, false
	}
	total, err := strconv.ParseInt(value[slash+1:], 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

func metaFromHeaders(bucket, key string, h http.Header, size int64) ObjectMeta {
	meta := ObjectMeta{
		Bucket:      bucket,
		Key:         key,
		VersionID:   h.Get(HeaderVersionID),
		SizeBytes:   size,
		ETag:        h.Get("ETag"),
		SHA256:      h.Get(HeaderContentSHA256),
		ContentType: h.Get("Content-Type"),
		Encrypted:   h.Get(HeaderEncrypted) == "true",
	}
	if modified, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
		meta.LastModified = modified.UTC().Truncate(time.Second)
	}
	prefix := http.CanonicalHeaderKey(HeaderMetaPrefix)
	for name, values := range h {
		if len(values) == 0 || !strings.HasPrefix(name, prefix) {
			continue
		}
		if meta.UserMetadata == nil {
			meta.UserMetadata = map[string]string{}
		}
		meta.UserMetadata[strings.ToLower(strings.TrimPrefix(name, prefix))] = values[0]
	}
	return meta
}
