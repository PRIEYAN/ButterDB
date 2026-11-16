package adminv1

import (
	"io"
	"time"
)

type ObjectSummary struct {
	Key          string    `json:"key"`
	SizeBytes    int64     `json:"size_bytes"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type"`
	Encrypted    bool      `json:"encrypted"`
}

type ObjectList struct {
	Bucket         string          `json:"bucket"`
	Prefix         string          `json:"prefix"`
	Delimiter      string          `json:"delimiter"`
	Objects        []ObjectSummary `json:"objects"`
	CommonPrefixes []string        `json:"common_prefixes"`
	NextCursor     string          `json:"next_cursor,omitempty"`
}

type ListObjectsOptions struct {
	Prefix    string
	Delimiter string
	Cursor    string
	Limit     int
}

type ObjectMeta struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	VersionID    string            `json:"version_id"`
	SizeBytes    int64             `json:"size_bytes"`
	StoredBytes  int64             `json:"stored_bytes"`
	ETag         string            `json:"etag"`
	SHA256       string            `json:"sha256"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	Encrypted    bool              `json:"encrypted"`
	UserMetadata map[string]string `json:"user_metadata"`
	Replicated   bool              `json:"replicated"`
}

type PutObjectOptions struct {
	ContentType    string
	Encryption     string
	UserMetadata   map[string]string
	ContentMD5     string
	ContentSHA256  string
	IfNoneMatchAny bool
	IfMatch        string
	IdempotencyKey string
}

type GetObjectOptions struct {
	RangeStart  int64
	RangeEnd    int64
	IfMatch     string
	IfNoneMatch string
}

type ObjectStream struct {
	Body          io.ReadCloser
	Status        int
	ContentLength int64
	ContentRange  string
	Meta          ObjectMeta
}

type DeleteObjectsRequest struct {
	Keys []string `json:"keys"`
}

type DeleteObjectsResult struct {
	Deleted []string            `json:"deleted"`
	Errors  []DeleteObjectError `json:"errors"`
}

type DeleteObjectError struct {
	Key       string `json:"key"`
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type PresignRequest struct {
	Key              string `json:"key"`
	AccessKeyID      string `json:"access_key_id"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

type PresignedURL struct {
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	ExpiresAt time.Time `json:"expires_at"`
}
