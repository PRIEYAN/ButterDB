package handlers

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func objectMetaFixture() object.ObjectMeta {
	return object.ObjectMeta{
		PutMsgID:      8812,
		StoredSize:    482113,
		PlaintextSize: 482113,
		ETag:          "9b2cf535f27731c974343645a3985328",
		SHA256:        strings.Repeat("e3", 32),
		ContentType:   "image/jpeg",
		UserMeta:      map[string]string{"camera": "x100v"},
		MsgDate:       testStart.Unix(),
	}
}

func uploadRequest(path, body string, headers map[string]string) request {
	return request{method: http.MethodPut, path: path, reader: strings.NewReader(body), length: int64(len(body)), headers: headers}
}

func TestObjectKeyFromQuery(t *testing.T) {
	keys := []string{
		"plain.txt",
		"a/b/c.txt",
		"with space and ? mark",
		"percent%25sign",
		"../../etc/passwd",
		"a/../b",
		"ünïcödé/日本語.png",
		"trailing/",
		"hash#fragment&amp=1",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			h := newHarness(t)
			h.lister.meta = objectMetaFixture()
			target := adminv1.BasePath + "/buckets/photos/object/meta?" + url.Values{adminv1.QueryKey: {key}}.Encode()
			w := h.do(request{method: http.MethodGet, path: target})
			if w.Code != 200 {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if len(h.lister.metaKeys) != 1 || string(h.lister.metaKeys[0]) != key {
				t.Fatalf("lister saw %q, want %q", h.lister.metaKeys, key)
			}
			if got := decodeBody[adminv1.ObjectMeta](t, w); got.Key != key || got.Bucket != "photos" {
				t.Fatalf("meta = %+v", got)
			}
		})
	}
	t.Run("key in the path does not route", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object/meta/a/b.txt"}), 404, adminv1.CodeNotFound)
	})
	invalid := []struct {
		name   string
		query  string
		status int
		code   adminv1.Code
	}{
		{"missing", "", 400, adminv1.CodeBadRequest},
		{"duplicated", "?key=a&key=b", 400, adminv1.CodeBadRequest},
		{"empty", "?key=", 422, adminv1.CodeObjectKeyInvalid},
		{"too long", "?key=" + strings.Repeat("k", 1025), 422, adminv1.CodeObjectKeyInvalid},
		{"invalid utf-8", "?key=%ff%fe", 422, adminv1.CodeObjectKeyInvalid},
		{"control character", "?key=a%00b", 422, adminv1.CodeObjectKeyInvalid},
	}
	for _, test := range invalid {
		t.Run("invalid "+test.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object/meta" + test.query})
			assertError(t, w, test.status, test.code)
			if len(h.lister.metaKeys) != 0 {
				t.Fatal("lister reached with an invalid key")
			}
		})
	}
}

func TestObjectListEndpoint(t *testing.T) {
	t.Run("maps the page and defaults", func(t *testing.T) {
		h := newHarness(t)
		h.lister.page = object.ListPage{
			Objects:        []object.ObjectMeta{objectMetaFixture()},
			CommonPrefixes: []string{"2026/11/raw/"},
			NextToken:      "next",
		}
		h.lister.page.Objects[0].Key = "2026/11/cat.jpg"
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/objects?prefix=2026%2F11%2F&delimiter=%2F"})
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		list := decodeBody[adminv1.ObjectList](t, w)
		if list.Bucket != "photos" || list.Prefix != "2026/11/" || list.Delimiter != "/" || list.NextCursor != "next" || len(list.CommonPrefixes) != 1 {
			t.Fatalf("list = %+v", list)
		}
		if len(list.Objects) != 1 || list.Objects[0].ETag != `"9b2cf535f27731c974343645a3985328"` || list.Objects[0].SizeBytes != 482113 {
			t.Fatalf("objects = %+v", list.Objects)
		}
		query := h.lister.queries[0]
		if query.Prefix != "2026/11/" || query.Delimiter != "/" || query.Limit != 200 || query.ContinuationToken != "" {
			t.Fatalf("query = %+v", query)
		}
	})
	t.Run("empty page encodes empty arrays", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/objects"})
		if !strings.Contains(w.Body.String(), `"objects":[]`) || !strings.Contains(w.Body.String(), `"common_prefixes":[]`) {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
	t.Run("cursor and limit are forwarded", func(t *testing.T) {
		h := newHarness(t)
		h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/objects?cursor=abc&limit=1000"})
		if q := h.lister.queries[0]; q.ContinuationToken != "abc" || q.Limit != 1000 {
			t.Fatalf("query = %+v", q)
		}
	})
	for _, query := range []string{"limit=0", "limit=1001", "limit=x", "delimiter=ab"} {
		t.Run("bad "+query, func(t *testing.T) {
			h := newHarness(t)
			assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/objects?" + query}), 400, adminv1.CodeBadRequest)
		})
	}
	t.Run("unknown bucket", func(t *testing.T) {
		h := newHarness(t)
		h.lister.err = errs.ErrNoSuchBucket
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/objects"}), 404, adminv1.CodeBucketNotFound)
	})
	t.Run("invalid bucket name", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/Bad_Name/objects"}), 422, adminv1.CodeBucketNameInvalid)
	})
}

func TestObjectMetaEndpoint(t *testing.T) {
	h := newHarness(t)
	h.lister.meta = objectMetaFixture()
	w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object/meta?key=cat.jpg"})
	meta := decodeBody[adminv1.ObjectMeta](t, w)
	if meta.VersionID != "8812" || meta.StoredBytes != 482113 || meta.ETag != `"9b2cf535f27731c974343645a3985328"` || meta.Replicated || meta.UserMetadata["camera"] != "x100v" {
		t.Fatalf("meta = %+v", meta)
	}
	t.Run("missing object", func(t *testing.T) {
		h := newHarness(t)
		h.lister.metaErr = errs.ErrNoSuchKey
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object/meta?key=x"}), 404, adminv1.CodeObjectNotFound)
	})
	t.Run("head error carries only the header", func(t *testing.T) {
		h := newHarness(t)
		h.lister.metaErr = errs.ErrNoSuchKey
		w := h.do(request{method: http.MethodHead, path: "/admin/v1/buckets/photos/object/meta?key=x"})
		if w.Code != 404 || w.Body.Len() != 0 || w.Header().Get(adminv1.HeaderErrorCode) != "object_not_found" {
			t.Fatalf("status %d body %q header %q", w.Code, w.Body.String(), w.Header().Get(adminv1.HeaderErrorCode))
		}
	})
}

func TestDownloadObject(t *testing.T) {
	t.Run("delegates to the object server", func(t *testing.T) {
		h := newHarness(t)
		h.server.body = "object-bytes"
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=a%2Fb"})
		if w.Code != 200 || w.Body.String() != "object-bytes" || string(h.server.served[0]) != "a/b" {
			t.Fatalf("status %d body %q served %v", w.Code, w.Body.String(), h.server.served)
		}
	})
	t.Run("head uses the same server", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodHead, path: "/admin/v1/buckets/photos/object?key=x"})
		if w.Code != 200 || w.Body.Len() != 0 || len(h.server.served) != 1 {
			t.Fatalf("status %d body %q served %v", w.Code, w.Body.String(), h.server.served)
		}
	})
	t.Run("error before any output becomes an envelope", func(t *testing.T) {
		h := newHarness(t)
		h.server.err = errs.ErrNoSuchKey
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=x"}), 404, adminv1.CodeObjectNotFound)
	})
	t.Run("unsatisfiable range", func(t *testing.T) {
		h := newHarness(t)
		h.server.err = fmt.Errorf("serve: %w", &httprange.UnsatisfiableRangeError{Requested: "bytes=9-", Size: 5})
		body := assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=x"}), 416, adminv1.CodeRangeNotSatisfiable)
		if body.Details["size_bytes"] != float64(5) {
			t.Fatalf("details = %v", body.Details)
		}
	})
	t.Run("error after output keeps the response", func(t *testing.T) {
		h := newHarness(t)
		h.server.status = http.StatusPartialContent
		h.server.err = nil
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=x"})
		if w.Code != 206 {
			t.Fatalf("status = %d", w.Code)
		}
	})
}

func TestUploadRequiresContentLength(t *testing.T) {
	h := newHarness(t)
	w := h.do(request{method: http.MethodPut, path: "/admin/v1/buckets/photos/object?key=a", reader: strings.NewReader("data"), length: -1})
	assertError(t, w, 411, adminv1.CodeLengthRequired)
	if len(h.writer.puts) != 0 {
		t.Fatal("writer reached without Content-Length")
	}
	t.Run("oversized declared length is rejected before reading", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPut, path: "/admin/v1/buckets/photos/object?key=a", reader: strings.NewReader("x"), length: 1992294401})
		assertError(t, w, 413, adminv1.CodeObjectTooLarge)
		if len(h.writer.puts) != 0 {
			t.Fatal("writer reached with an oversized length")
		}
	})
}

type commitProbeWriter struct {
	*httptest.ResponseRecorder
	committedAtHeader *bool
	committed         func() bool
}

func (c *commitProbeWriter) WriteHeader(status int) {
	if c.committedAtHeader != nil && status == http.StatusOK {
		*c.committedAtHeader = c.committed()
	}
	c.ResponseRecorder.WriteHeader(status)
}

func TestUploadAcknowledgedAfterCommit(t *testing.T) {
	h := newHarness(t)
	h.writer.meta = objectMetaFixture()
	h.writer.gate = make(chan struct{})
	var ackSawCommit bool
	probe := &commitProbeWriter{
		ResponseRecorder:  httptest.NewRecorder(),
		committedAtHeader: &ackSawCommit,
		committed: func() bool {
			h.writer.mu.Lock()
			defer h.writer.mu.Unlock()
			return h.writer.committed
		},
	}
	r := httptest.NewRequest(http.MethodPut, "/admin/v1/buckets/photos/object?key=cat.jpg", strings.NewReader("pixels"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.ServeHTTP(probe, r)
	}()
	select {
	case <-done:
		t.Fatal("the upload was acknowledged while the commit was still blocked")
	case <-time.After(50 * time.Millisecond):
	}
	close(h.writer.gate)
	<-done
	if probe.Code != 200 || !ackSawCommit {
		t.Fatalf("status %d acknowledged-after-commit %v", probe.Code, ackSawCommit)
	}
	meta := decodeBody[adminv1.ObjectMeta](t, probe.ResponseRecorder)
	if meta.Key != "cat.jpg" || meta.VersionID != "8812" || meta.Bucket != "photos" {
		t.Fatalf("meta = %+v", meta)
	}
	if string(h.writer.bodies[0]) != "pixels" {
		t.Fatalf("body = %q", h.writer.bodies[0])
	}
}

func TestUploadRequestMapping(t *testing.T) {
	h := newHarness(t)
	h.writer.meta = objectMetaFixture()
	sum := md5.Sum([]byte("pixels"))
	sha := sha256.Sum256([]byte("pixels"))
	w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=cat.jpg", "pixels", map[string]string{
		"Content-Type":                      "image/jpeg",
		adminv1.HeaderEncryption:            "tde1",
		adminv1.HeaderMetaPrefix + "Camera": "x100v",
		adminv1.HeaderMetaPrefix + "Roll":   "7",
		"Content-MD5":                       base64.StdEncoding.EncodeToString(sum[:]),
		adminv1.HeaderContentSHA256:         hex.EncodeToString(sha[:]),
		"If-None-Match":                     "*",
		"If-Match":                          ` "abc" `,
		adminv1.HeaderIdempotencyKey:        "upload-1",
	}))
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	req := h.writer.puts[0]
	if req.Bucket != "photos" || req.Key != "cat.jpg" || req.ContentLength != 6 || req.ContentType != "image/jpeg" || req.Encryption != "tde1" {
		t.Fatalf("request = %+v", req)
	}
	if req.UserMeta["camera"] != "x100v" || req.UserMeta["roll"] != "7" || len(req.UserMeta) != 2 {
		t.Fatalf("user meta = %v", req.UserMeta)
	}
	if !req.IfNoneMatchAny || req.IfMatch != `"abc"` || req.IdempotencyKey != "upload-1" {
		t.Fatalf("conditions = %+v", req)
	}
	if req.AccessKeyID != "device:"+h.device.DeviceID {
		t.Fatalf("caller identity = %q, want the device scope", req.AccessKeyID)
	}
}

func TestUploadHeaderValidation(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		status  int
		code    adminv1.Code
	}{
		{"bad encryption", map[string]string{adminv1.HeaderEncryption: "aes"}, 422, adminv1.CodeValidationFailed},
		{"if-none-match other than star", map[string]string{"If-None-Match": `"abc"`}, 422, adminv1.CodeValidationFailed},
		{"idempotency key too long", map[string]string{adminv1.HeaderIdempotencyKey: strings.Repeat("k", 129)}, 422, adminv1.CodeValidationFailed},
		{"idempotency key not printable", map[string]string{adminv1.HeaderIdempotencyKey: "bad\x01key"}, 422, adminv1.CodeValidationFailed},
		{"bad content md5", map[string]string{"Content-MD5": "!!!"}, 400, adminv1.CodeBadRequest},
		{"short content md5", map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString([]byte("short"))}, 400, adminv1.CodeBadRequest},
		{"bad sha256", map[string]string{adminv1.HeaderContentSHA256: "abcd"}, 400, adminv1.CodeBadRequest},
		{"metadata too large", map[string]string{adminv1.HeaderMetaPrefix + "big": strings.Repeat("v", 2100)}, 422, adminv1.CodeMetadataTooLarge},
		{"metadata name empty", map[string]string{adminv1.HeaderMetaPrefix: "v"}, 400, adminv1.CodeBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", test.headers))
			assertError(t, w, test.status, test.code)
			if len(h.writer.puts) != 0 {
				t.Fatal("writer reached despite invalid headers")
			}
		})
	}
}

func TestUploadChecksumMismatch(t *testing.T) {
	good := md5.Sum([]byte("pixels"))
	tests := []struct {
		name      string
		headers   map[string]string
		algorithm string
	}{
		{"md5", map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(good[:])}, "md5"},
		{"sha256", map[string]string{adminv1.HeaderContentSHA256: strings.Repeat("0", 64)}, "sha256"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			body := "tampered"
			w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", body, test.headers))
			got := assertError(t, w, 400, adminv1.CodeChecksumMismatch)
			if got.Details["algorithm"] != test.algorithm {
				t.Fatalf("details = %v", got.Details)
			}
		})
	}
	t.Run("matching digests pass", func(t *testing.T) {
		h := newHarness(t)
		h.writer.meta = objectMetaFixture()
		sha := sha256.Sum256([]byte("pixels"))
		w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "pixels", map[string]string{
			"Content-MD5":               base64.StdEncoding.EncodeToString(good[:]),
			adminv1.HeaderContentSHA256: hex.EncodeToString(sha[:]),
		}))
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	})
}

type idempotentStore struct {
	mu      sync.Mutex
	seen    map[string]string
	metas   map[string]object.ObjectMeta
	sends   int
	nextMsg int64
}

func (s *idempotentStore) put(req object.PutRequest, body []byte) (object.ObjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]string{}
		s.metas = map[string]object.ObjectMeta{}
	}
	fingerprint := fmt.Sprintf("%s|%s|%x", req.Bucket, req.Key, sha256.Sum256(body))
	if req.IdempotencyKey != "" {
		scoped := req.AccessKeyID + "|" + req.IdempotencyKey
		if previous, ok := s.seen[scoped]; ok {
			if previous != fingerprint {
				return object.ObjectMeta{}, fmt.Errorf("put: %w", put.ErrIdempotencyConflict)
			}
			return s.metas[scoped], nil
		}
		defer func() { s.seen[scoped] = fingerprint }()
	}
	s.sends++
	s.nextMsg++
	meta := objectMetaFixture()
	meta.Key = string(req.Key)
	meta.PutMsgID = 9000 + s.nextMsg
	if req.IdempotencyKey != "" {
		s.metas[req.AccessKeyID+"|"+req.IdempotencyKey] = meta
	}
	return meta, nil
}

func TestUploadIdempotentReplay(t *testing.T) {
	h := newHarness(t)
	store := &idempotentStore{}
	h.writer.onPut = store.put
	headers := map[string]string{adminv1.HeaderIdempotencyKey: "same-key"}
	first := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "payload", headers))
	second := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "payload", headers))
	if first.Code != 200 || second.Code != 200 {
		t.Fatalf("statuses %d %d: %s", first.Code, second.Code, second.Body.String())
	}
	if store.sends != 1 {
		t.Fatalf("sends = %d, want 1", store.sends)
	}
	a, b := decodeBody[adminv1.ObjectMeta](t, first), decodeBody[adminv1.ObjectMeta](t, second)
	if a.VersionID != b.VersionID {
		t.Fatalf("replay returned a different version: %s vs %s", a.VersionID, b.VersionID)
	}
	t.Run("scoped by device", func(t *testing.T) {
		h.device.DeviceID = "otherdeviceid0000"
		third := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "payload", headers))
		if third.Code != 200 || store.sends != 2 {
			t.Fatalf("status %d sends %d: a second device must not hit the first device's record", third.Code, store.sends)
		}
	})
}

func TestUploadIdempotencyConflict(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"different body", "/admin/v1/buckets/photos/object?key=a", "other body", 409},
		{"different key", "/admin/v1/buckets/photos/object?key=b", "payload", 409},
		{"different bucket", "/admin/v1/buckets/avatars/object?key=a", "payload", 409},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			store := &idempotentStore{}
			h.writer.onPut = store.put
			headers := map[string]string{adminv1.HeaderIdempotencyKey: "reused"}
			if w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "payload", headers)); w.Code != 200 {
				t.Fatalf("first upload status = %d", w.Code)
			}
			w := h.do(uploadRequest(test.path, test.body, headers))
			assertError(t, w, test.status, adminv1.CodeIdempotencyConflict)
			if store.sends != 1 {
				t.Fatalf("sends = %d, want 1", store.sends)
			}
		})
	}
}

func TestUploadCommitOutcomeUnknown(t *testing.T) {
	h := newHarness(t)
	h.writer.putErr = fmt.Errorf("put: %w", &put.OutcomeUnknownError{JobID: "job-9", Err: tgclient.ErrSendOutcomeUnknown})
	w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", map[string]string{adminv1.HeaderIdempotencyKey: "retry-me"}))
	body := assertError(t, w, 503, adminv1.CodeCommitOutcomeUnknown)
	if !body.Retryable || body.Details["job_id"] != "job-9" || body.RetryAfterSeconds <= 0 {
		t.Fatalf("body = %+v", body)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
	if len(h.audit.records) != 1 || h.audit.records[0].Action != "object.upload" || h.audit.records[0].Outcome != "failure" || h.audit.records[0].ErrorCode != "commit_outcome_unknown" {
		t.Fatalf("audit = %+v", h.audit.records)
	}
	t.Run("flood wait is retryable with a delay", func(t *testing.T) {
		h := newHarness(t)
		h.writer.putErr = &tgclient.FloodWaitError{Duration: 42 * time.Second}
		w := h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", nil))
		body := assertError(t, w, 503, adminv1.CodeTelegramFloodWait)
		if body.RetryAfterSeconds != 42 || w.Header().Get("Retry-After") != "42" {
			t.Fatalf("body = %+v header %q", body, w.Header().Get("Retry-After"))
		}
	})
	t.Run("too large from the writer", func(t *testing.T) {
		h := newHarness(t)
		h.writer.putErr = errs.ErrObjectTooLarge
		assertError(t, h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", nil)), 413, adminv1.CodeObjectTooLarge)
	})
	t.Run("precondition failed", func(t *testing.T) {
		h := newHarness(t)
		h.writer.putErr = errs.ErrPreconditionFailed
		assertError(t, h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", nil)), 412, adminv1.CodePreconditionFailed)
	})
	t.Run("unknown bucket", func(t *testing.T) {
		h := newHarness(t)
		h.writer.putErr = errs.ErrNoSuchBucket
		assertError(t, h.do(uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", nil)), 404, adminv1.CodeBucketNotFound)
	})
}

func TestDeleteMissingObject204(t *testing.T) {
	t.Run("writer reports no such key", func(t *testing.T) {
		h := newHarness(t)
		h.writer.deleteErrs = map[object.Key]error{"ghost": fmt.Errorf("remove: %w", errs.ErrNoSuchKey)}
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos/object?key=ghost"})
		if w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("status %d body %q", w.Code, w.Body.String())
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Outcome != "success" || h.audit.records[0].Action != "object.delete" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("writer succeeds", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos/object?key=real"})
		if w.Code != 204 || h.writer.deletes[0] != "real" {
			t.Fatalf("status %d deletes %v", w.Code, h.writer.deletes)
		}
	})
	t.Run("other failures surface", func(t *testing.T) {
		h := newHarness(t)
		h.writer.deleteErrs = map[object.Key]error{"k": &tgclient.FloodWaitError{Duration: time.Second}}
		assertError(t, h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos/object?key=k"}), 503, adminv1.CodeTelegramFloodWait)
	})
}

func TestBatchDeletePartialFailure(t *testing.T) {
	h := newHarness(t)
	h.writer.deleteErrs = map[object.Key]error{
		"dog.jpg":   &tgclient.FloodWaitError{Duration: 9 * time.Second},
		"ghost.jpg": errs.ErrNoSuchKey,
	}
	w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/objects/delete", body: adminv1.DeleteObjectsRequest{Keys: []string{"cat.jpg", "dog.jpg", "ghost.jpg", ""}}})
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	result := decodeBody[adminv1.DeleteObjectsResult](t, w)
	if strings.Join(result.Deleted, ",") != "cat.jpg,ghost.jpg" {
		t.Fatalf("deleted = %v", result.Deleted)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("errors = %+v", result.Errors)
	}
	if e := result.Errors[0]; e.Key != "dog.jpg" || e.Code != adminv1.CodeTelegramFloodWait || !e.Retryable || e.Message == "" {
		t.Fatalf("first error = %+v", e)
	}
	if e := result.Errors[1]; e.Key != "" || e.Code != adminv1.CodeObjectKeyInvalid || e.Retryable {
		t.Fatalf("second error = %+v", e)
	}
	if got := h.writer.deletes; len(got) != 3 || got[0] != "cat.jpg" || got[1] != "dog.jpg" || got[2] != "ghost.jpg" {
		t.Fatalf("deletes happened out of order or skipped: %v", got)
	}
	if len(h.audit.records) != 1 || h.audit.records[0].Action != "object.delete_batch" || h.audit.records[0].Outcome != "partial" || h.audit.records[0].ErrorCode != "telegram_flood_wait" {
		t.Fatalf("audit = %+v", h.audit.records)
	}
	t.Run("all deleted", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/objects/delete", body: adminv1.DeleteObjectsRequest{Keys: []string{"a", "b"}}})
		if !strings.Contains(w.Body.String(), `"errors":[]`) || h.audit.records[0].Outcome != "success" {
			t.Fatalf("body %s audit %+v", w.Body.String(), h.audit.records)
		}
	})
	t.Run("key count bounds", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/objects/delete", body: adminv1.DeleteObjectsRequest{Keys: nil}}), 422, adminv1.CodeValidationFailed)
		many := make([]string, 1001)
		for i := range many {
			many[i] = fmt.Sprintf("k%d", i)
		}
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/objects/delete", body: adminv1.DeleteObjectsRequest{Keys: many}}), 422, adminv1.CodeValidationFailed)
		if len(h.writer.deletes) != 0 {
			t.Fatal("keys were deleted from an invalid batch")
		}
	})
}

func presignKeyFixture(buckets, actions []string, revoked bool) access.KeyInfo {
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		panic(err)
	}
	info := access.KeyInfo{ID: "BDBK7Q2M9X4T1W8Z3R6Y", Name: "site", Scope: scope, CreatedAt: 1700000000000}
	if revoked {
		info.RevokedAt = 1700000100000
	}
	return info
}

func TestPresignRequiresReadScope(t *testing.T) {
	body := adminv1.PresignRequest{Key: "cat.jpg", AccessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y", ExpiresInSeconds: 3600}
	tests := []struct {
		name string
		key  *access.KeyInfo
		code adminv1.Code
	}{
		{"list only", ptr(presignKeyFixture([]string{"photos"}, []string{"list"}, false)), adminv1.CodePresignKeyInvalid},
		{"write only", ptr(presignKeyFixture([]string{"photos"}, []string{"write"}, false)), adminv1.CodePresignKeyInvalid},
		{"read on another bucket", ptr(presignKeyFixture([]string{"avatars"}, []string{"read"}, false)), adminv1.CodePresignKeyInvalid},
		{"revoked key", ptr(presignKeyFixture([]string{"photos"}, []string{"read"}, true)), adminv1.CodePresignKeyInvalid},
		{"unknown key", nil, adminv1.CodePresignKeyInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.lister.meta = objectMetaFixture()
			if test.key != nil {
				h.keys.put(*test.key)
			}
			w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: body})
			assertError(t, w, 422, test.code)
			if h.presign.calls != 0 {
				t.Fatal("presigner reached for a key without read on the bucket")
			}
		})
	}
	allowed := []struct {
		name    string
		buckets []string
	}{
		{"read on the bucket", []string{"photos"}},
		{"read on every bucket", []string{"*"}},
	}
	for _, test := range allowed {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.lister.meta = objectMetaFixture()
			h.keys.put(presignKeyFixture(test.buckets, []string{"read", "list"}, false))
			w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: body})
			if w.Code != 200 {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			got := decodeBody[adminv1.PresignedURL](t, w)
			if got.Method != "GET" || got.URL != h.presign.url || !got.ExpiresAt.Equal(h.presign.expires) || h.presign.ttl != time.Hour {
				t.Fatalf("presigned = %+v ttl %v", got, h.presign.ttl)
			}
			if len(h.audit.records) != 1 || h.audit.records[0].Action != "object.presign" || strings.Contains(h.audit.records[0].Target, "X-Amz") {
				t.Fatalf("audit = %+v", h.audit.records)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestPresignRules(t *testing.T) {
	good := adminv1.PresignRequest{Key: "cat.jpg", AccessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y"}
	setup := func(t *testing.T) *harness {
		h := newHarness(t)
		h.lister.meta = objectMetaFixture()
		h.keys.put(presignKeyFixture([]string{"photos"}, []string{"read"}, false))
		return h
	}
	t.Run("default expiry is one hour", func(t *testing.T) {
		h := setup(t)
		if w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: good}); w.Code != 200 || h.presign.ttl != time.Hour {
			t.Fatalf("status %d ttl %v", w.Code, h.presign.ttl)
		}
	})
	for _, seconds := range []int{-1, 604801} {
		t.Run(fmt.Sprintf("expiry %d rejected", seconds), func(t *testing.T) {
			h := setup(t)
			req := good
			req.ExpiresInSeconds = seconds
			assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: req}), 422, adminv1.CodeValidationFailed)
		})
	}
	t.Run("maximum expiry accepted", func(t *testing.T) {
		h := setup(t)
		req := good
		req.ExpiresInSeconds = 604800
		if w := h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: req}); w.Code != 200 || h.presign.ttl != 604800*time.Second {
			t.Fatalf("status %d ttl %v", w.Code, h.presign.ttl)
		}
	})
	t.Run("missing object", func(t *testing.T) {
		h := setup(t)
		h.lister.metaErr = errs.ErrNoSuchKey
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: good}), 404, adminv1.CodeObjectNotFound)
		if h.presign.calls != 0 {
			t.Fatal("presigned a missing object")
		}
	})
	t.Run("s3 public url unset", func(t *testing.T) {
		h := setup(t)
		h.presign.err = ErrS3PublicURLUnset
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: good}), 409, adminv1.CodeS3PublicURLUnset)
	})
	t.Run("malformed access key id", func(t *testing.T) {
		h := setup(t)
		req := good
		req.AccessKeyID = "not-a-key"
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: req}), 422, adminv1.CodePresignKeyInvalid)
	})
	t.Run("invalid key", func(t *testing.T) {
		h := setup(t)
		req := good
		req.Key = ""
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: req}), 422, adminv1.CodeObjectKeyInvalid)
	})
	t.Run("key service failure surfaces", func(t *testing.T) {
		h := setup(t)
		h.keys.err = errors.New("db down")
		assertError(t, h.do(request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: good}), 500, adminv1.CodeInternal)
	})
}
