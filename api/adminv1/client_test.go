package adminv1_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type recorded struct {
	method string
	path   string
	query  string
	body   string
	header http.Header
}

type recorder struct {
	mu    sync.Mutex
	calls []recorded
}

func (r *recorder) last() recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func newPlainClient(t *testing.T, handler http.HandlerFunc, token string) (*adminv1.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.calls = append(rec.calls, recorded{
			method: r.Method,
			path:   r.URL.EscapedPath(),
			query:  r.URL.RawQuery,
			body:   string(body),
			header: r.Header.Clone(),
		})
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, Token: token})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, rec
}

func okJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestAPIErrorDecoding(t *testing.T) {
	envelope := `{"error":{"code":"telegram_code_invalid","message":"Telegram rejected the login code.","retryable":true,"hint":"Check the code.","retry_after_seconds":7,"details":{"attempts_left":4},"request_id":"req-body"}}`
	cases := []struct {
		name       string
		status     int
		header     map[string]string
		body       string
		wantStatus int
		wantCode   adminv1.Code
		wantRetry  bool
		wantAfter  time.Duration
		wantReqID  string
	}{
		{name: "full envelope", status: 400, body: envelope, wantStatus: 400, wantCode: adminv1.CodeTelegramCodeInvalid, wantRetry: true, wantAfter: 7 * time.Second, wantReqID: "req-body"},
		{name: "request id from header", status: 404, header: map[string]string{"X-Request-Id": "req-header"}, body: `{"error":{"code":"bucket_not_found","message":"no","retryable":false,"hint":""}}`, wantStatus: 404, wantCode: adminv1.CodeBucketNotFound, wantReqID: "req-header"},
		{name: "retry after header", status: 429, header: map[string]string{"Retry-After": "12"}, body: `{"error":{"code":"rate_limited","message":"slow","retryable":true}}`, wantStatus: 429, wantCode: adminv1.CodeRateLimited, wantRetry: true, wantAfter: 12 * time.Second},
		{name: "html gateway error", status: 502, body: `<html>bad gateway</html>`, wantStatus: 502, wantCode: adminv1.CodeInternal, wantRetry: true},
		{name: "empty unauthorized", status: 401, wantStatus: 401, wantCode: adminv1.CodeUnauthenticated},
		{name: "not implemented", status: 501, body: `{"error":{"code":"not_implemented","message":"later","details":{"phase":12}}}`, wantStatus: 501, wantCode: adminv1.CodeNotImplemented},
		{name: "unknown code kept", status: 409, body: `{"error":{"code":"from_the_future","message":"new"}}`, wantStatus: 409, wantCode: "from_the_future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) {
				for name, value := range tc.header {
					w.Header().Set(name, value)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}, "tok")
			_, err := client.Status(context.Background())
			var apiErr *adminv1.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *APIError", err)
			}
			if apiErr.Status != tc.wantStatus || apiErr.Body.Code != tc.wantCode || apiErr.Body.Retryable != tc.wantRetry {
				t.Fatalf("got %+v", apiErr)
			}
			if tc.wantReqID != "" && apiErr.Body.RequestID != tc.wantReqID {
				t.Fatalf("request id = %q", apiErr.Body.RequestID)
			}
			if !adminv1.IsCode(err, tc.wantCode) {
				t.Fatal("IsCode false")
			}
			after, ok := adminv1.RetryAfter(err)
			if ok != (tc.wantAfter > 0) || after != tc.wantAfter {
				t.Fatalf("retry after = %v %v", after, ok)
			}
		})
	}
}

func TestAPIErrorDetailsDecoded(t *testing.T) {
	client, _ := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":{"code":"not_implemented","message":"later","details":{"phase":12}}}`))
	}, "tok")
	_, err := client.ReplicaStatus(context.Background())
	var apiErr *adminv1.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v", err)
	}
	if apiErr.Body.Details["phase"] != float64(12) {
		t.Fatalf("details = %v", apiErr.Body.Details)
	}
}

func TestHeadErrorFromHeader(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		code     string
		retryHdr string
		wantCode adminv1.Code
		wantWait time.Duration
	}{
		{name: "object not found", status: 404, code: "object_not_found", wantCode: adminv1.CodeObjectNotFound},
		{name: "flood wait", status: 503, code: "telegram_flood_wait", retryHdr: "9", wantCode: adminv1.CodeTelegramFloodWait, wantWait: 9 * time.Second},
		{name: "no code header", status: 404, wantCode: adminv1.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, rec := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.code != "" {
					w.Header().Set(adminv1.HeaderErrorCode, tc.code)
				}
				if tc.retryHdr != "" {
					w.Header().Set("Retry-After", tc.retryHdr)
				}
				w.Header().Set(adminv1.HeaderRequestID, "rid-1")
				w.WriteHeader(tc.status)
			}, "tok")
			_, err := client.HeadObject(context.Background(), "photos", "a.jpg")
			var apiErr *adminv1.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *APIError", err)
			}
			if apiErr.Status != tc.status || apiErr.Body.Code != tc.wantCode || apiErr.Body.RequestID != "rid-1" {
				t.Fatalf("got %+v", apiErr)
			}
			if after, _ := adminv1.RetryAfter(err); after != tc.wantWait {
				t.Fatalf("retry after = %v", after)
			}
			if got := rec.last().method; got != http.MethodHead {
				t.Fatalf("method = %s", got)
			}
		})
	}
}

func TestHeadObjectParsesDownloadHeaders(t *testing.T) {
	client, _ := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", "482113")
		h.Set("ETag", `"9b2cf535f27731c974343645a3985328"`)
		h.Set("Last-Modified", "Thu, 12 Nov 2026 09:41:07 GMT")
		h.Set(adminv1.HeaderEncrypted, "true")
		h.Set(adminv1.HeaderVersionID, "8812")
		h.Set(adminv1.HeaderMetaPrefix+"camera", "x100v")
		h.Set(adminv1.HeaderMetaPrefix+"Owner", "me")
	}, "tok")
	meta, err := client.HeadObject(context.Background(), "photos", "2026/cat.jpg")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	want := adminv1.ObjectMeta{
		Bucket:       "photos",
		Key:          "2026/cat.jpg",
		VersionID:    "8812",
		SizeBytes:    482113,
		ETag:         `"9b2cf535f27731c974343645a3985328"`,
		ContentType:  "image/jpeg",
		LastModified: time.Date(2026, 11, 12, 9, 41, 7, 0, time.UTC),
		Encrypted:    true,
		UserMetadata: map[string]string{"camera": "x100v", "owner": "me"},
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %+v\nwant   %+v", meta, want)
	}
}

func TestPutObjectStreamsWithoutBuffering(t *testing.T) {
	const firstChunk = 16 << 10
	const secondChunk = 32 << 10
	const probeBytes = 8 << 10
	type seen struct {
		contentLength    int64
		transferEncoding []string
		header           http.Header
		query            string
		total            int
	}
	run := func(t *testing.T, newClient func(t *testing.T, handler http.HandlerFunc) *adminv1.Client) {
		firstSeen := make(chan struct{})
		release := make(chan struct{})
		result := make(chan seen, 1)
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, probeBytes)
			if _, err := io.ReadFull(r.Body, buf); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			close(firstSeen)
			rest, _ := io.ReadAll(r.Body)
			result <- seen{r.ContentLength, r.TransferEncoding, r.Header.Clone(), r.URL.RawQuery, probeBytes + len(rest)}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bucket":"photos","key":"big.bin","size_bytes":49152,"version_id":"77"}`))
		})
		reader := &gatedReader{first: firstChunk, second: secondChunk, release: release}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() {
			select {
			case <-firstSeen:
				close(release)
			case <-ctx.Done():
			}
		}()
		meta, err := client.PutObject(ctx, "photos", "big.bin", reader, firstChunk+secondChunk, adminv1.PutObjectOptions{
			ContentType:    "application/octet-stream",
			Encryption:     "tde1",
			UserMetadata:   map[string]string{"camera": "x100v"},
			ContentMD5:     "md5b64",
			ContentSHA256:  "abcd",
			IfNoneMatchAny: true,
			IdempotencyKey: "idem-1",
		})
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		if meta.VersionID != "77" || meta.SizeBytes != 49152 {
			t.Fatalf("meta = %+v", meta)
		}
		got := <-result
		if got.total != firstChunk+secondChunk {
			t.Fatalf("server read %d bytes", got.total)
		}
		if got.contentLength != firstChunk+secondChunk || len(got.transferEncoding) != 0 {
			t.Fatalf("content length = %d, transfer encoding = %v", got.contentLength, got.transferEncoding)
		}
		if got.query != (url.Values{"key": {"big.bin"}}).Encode() {
			t.Fatalf("query = %q", got.query)
		}
		wantHeaders := map[string]string{
			"Content-Type":                      "application/octet-stream",
			adminv1.HeaderEncryption:            "tde1",
			adminv1.HeaderMetaPrefix + "camera": "x100v",
			"Content-Md5":                       "md5b64",
			adminv1.HeaderContentSHA256:         "abcd",
			"If-None-Match":                     "*",
			adminv1.HeaderIdempotencyKey:        "idem-1",
			adminv1.HeaderAuthorization:         adminv1.BearerPrefix + "tok",
		}
		for name, want := range wantHeaders {
			if v := got.header.Get(name); v != want {
				t.Fatalf("header %s = %q, want %q", name, v, want)
			}
		}
	}
	t.Run("http2 over pinned tls", func(t *testing.T) {
		run(t, func(t *testing.T, handler http.HandlerFunc) *adminv1.Client {
			fixture := newTLSFixture(t, handler)
			client := pinnedClient(t, fixture.server.URL, fixture.pin)
			client.SetToken("tok")
			return client
		})
	})
	t.Run("http1 over loopback", func(t *testing.T) {
		run(t, func(t *testing.T, handler http.HandlerFunc) *adminv1.Client {
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, Token: "tok"})
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			return client
		})
	})
}

type gatedReader struct {
	first   int
	second  int
	release chan struct{}
	sent    int
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if g.sent == g.first {
		select {
		case <-g.release:
		case <-time.After(5 * time.Second):
			return 0, errors.New("body was drained before the server saw the first chunk")
		}
	}
	limit := g.first
	if g.sent >= g.first {
		limit = g.first + g.second
	}
	if g.sent >= limit {
		return 0, io.EOF
	}
	n := min(len(p), limit-g.sent)
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	g.sent += n
	return n, nil
}

func TestPutObjectSizeHandling(t *testing.T) {
	client, rec := newPlainClient(t, okJSON(`{"key":"k"}`), "tok")
	ctx := context.Background()
	t.Run("zero size sends explicit content length", func(t *testing.T) {
		if _, err := client.PutObject(ctx, "b", "empty", nil, 0, adminv1.PutObjectOptions{}); err != nil {
			t.Fatalf("put: %v", err)
		}
		call := rec.last()
		if call.header.Get("Content-Length") != "0" || call.body != "" {
			t.Fatalf("headers = %v body = %q", call.header, call.body)
		}
	})
	t.Run("negative size", func(t *testing.T) {
		if _, err := client.PutObject(ctx, "b", "k", strings.NewReader("x"), -1, adminv1.PutObjectOptions{}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("nil body with size", func(t *testing.T) {
		if _, err := client.PutObject(ctx, "b", "k", nil, 5, adminv1.PutObjectOptions{}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("body shorter than size", func(t *testing.T) {
		if _, err := client.PutObject(ctx, "b", "k", strings.NewReader("abc"), 10, adminv1.PutObjectOptions{}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("server error becomes api error", func(t *testing.T) {
		failing, _ := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"commit_outcome_unknown","message":"unknown","retryable":true,"details":{"job_id":"j1"}}}`))
		}, "tok")
		_, err := failing.PutObject(ctx, "b", "k", strings.NewReader("abc"), 3, adminv1.PutObjectOptions{})
		if !adminv1.IsCode(err, adminv1.CodeCommitOutcomeUnknown) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestTokenNotInErrorText(t *testing.T) {
	const token = "bdt1_abcdefghijklmnop_secretsecretsecretsecretsecretsecretsecretsecret"
	ctx := context.Background()
	t.Run("api error", func(t *testing.T) {
		client, _ := newPlainClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			echo := strings.TrimPrefix(r.Header.Get(adminv1.HeaderAuthorization), adminv1.BearerPrefix)
			payload, _ := json.Marshal(adminv1.ErrorEnvelope{Error: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated, Message: "bad " + echo, Hint: echo}})
			_, _ = w.Write(payload)
		}, token)
		_, err := client.Status(ctx)
		if err == nil || strings.Contains(err.Error(), token) {
			t.Fatalf("error = %v", err)
		}
		var apiErr *adminv1.APIError
		if !errors.As(err, &apiErr) || strings.Contains(apiErr.Body.Hint, token) {
			t.Fatalf("hint leaked: %+v", apiErr)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(okJSON(`{}`)))
		base := server.URL
		server.Close()
		client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: base, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for name, run := range map[string]func() error{
			"status": func() error { _, err := client.Status(ctx); return err },
			"head":   func() error { _, err := client.HeadObject(ctx, "b", "k"); return err },
			"get":    func() error { _, err := client.GetObject(ctx, "b", "k", adminv1.GetObjectOptions{}); return err },
			"put": func() error {
				_, err := client.PutObject(ctx, "b", "k", strings.NewReader("x"), 1, adminv1.PutObjectOptions{})
				return err
			},
		} {
			if err := run(); err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("%s error = %v", name, err)
			}
		}
	})
	t.Run("replaced token is used", func(t *testing.T) {
		client, rec := newPlainClient(t, okJSON(`{}`), token)
		client.SetToken("second")
		if _, err := client.Status(ctx); err != nil {
			t.Fatal(err)
		}
		if got := rec.last().header.Get(adminv1.HeaderAuthorization); got != "Bearer second" {
			t.Fatalf("authorization = %q", got)
		}
	})
}

func TestSetTokenConcurrent(t *testing.T) {
	client, _ := newPlainClient(t, okJSON(`{}`), "a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				client.SetToken("tok")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := client.Status(context.Background()); err != nil {
					t.Errorf("status: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestAnonymousRoutesOmitToken(t *testing.T) {
	client, rec := newPlainClient(t, okJSON(`{}`), "tok")
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() error
		auth bool
	}{
		{name: "server info", run: func() error { _, err := client.ServerInfo(ctx); return err }},
		{name: "claim", run: func() error { _, err := client.Claim(ctx, adminv1.ClaimRequest{}); return err }},
		{name: "pairing request", run: func() error { _, err := client.RequestPairing(ctx, adminv1.PairingRequest{}); return err }},
		{name: "pairing complete", run: func() error { _, err := client.CompletePairing(ctx, adminv1.PairingCompleteRequest{}); return err }},
		{name: "devices", run: func() error { _, err := client.ListDevices(ctx); return err }, auth: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			got := rec.last().header.Get(adminv1.HeaderAuthorization)
			if tc.auth && got != "Bearer tok" {
				t.Fatalf("authorization = %q", got)
			}
			if !tc.auth && got != "" {
				t.Fatalf("authorization = %q on anonymous route", got)
			}
		})
	}
}

func TestRequestShapes(t *testing.T) {
	client, rec := newPlainClient(t, okJSON(`{}`), "tok")
	ctx := context.Background()
	one := 3600
	name := "renamed"
	since := time.Date(2026, 11, 1, 10, 30, 0, 0, time.FixedZone("x", 3600))
	cases := []struct {
		name       string
		run        func() error
		wantMethod string
		wantPath   string
		wantQuery  string
		wantBody   string
	}{
		{"rename device", func() error {
			_, err := client.RenameDevice(ctx, "dev/1", adminv1.UpdateDeviceRequest{Name: "n"})
			return err
		}, "PATCH", "/admin/v1/devices/dev%2F1", "", `{"name":"n"}`},
		{"revoke device", func() error { return client.RevokeDevice(ctx, "d1", false) }, "DELETE", "/admin/v1/devices/d1", "", ""},
		{"revoke last device", func() error { return client.RevokeDevice(ctx, "d1", true) }, "DELETE", "/admin/v1/devices/d1", "confirm_last=true", ""},
		{"rotate", func() error { _, err := client.RotateDeviceToken(ctx); return err }, "POST", "/admin/v1/devices/self/rotate", "", ""},
		{"telegram status", func() error { _, err := client.TelegramStatus(ctx); return err }, "GET", "/admin/v1/setup/telegram", "", ""},
		{"save credentials", func() error {
			_, err := client.SaveTelegramCredentials(ctx, adminv1.TelegramCredentialsRequest{APIID: 5, APIHash: "h"})
			return err
		}, "POST", "/admin/v1/setup/telegram/app", "", `{"api_id":5,"api_hash":"h"}`},
		{"delete credentials", func() error { _, err := client.DeleteTelegramCredentials(ctx); return err }, "DELETE", "/admin/v1/setup/telegram/app", "", ""},
		{"start login", func() error {
			_, err := client.StartTelegramLogin(ctx, adminv1.TelegramLoginStartRequest{Phone: "+1"})
			return err
		}, "POST", "/admin/v1/setup/telegram/phone", "", `{"phone":"+1"}`},
		{"submit code", func() error {
			_, err := client.SubmitTelegramCode(ctx, adminv1.TelegramCodeRequest{LoginID: "l", Code: "1"})
			return err
		}, "POST", "/admin/v1/setup/telegram/code", "", `{"login_id":"l","code":"1"}`},
		{"submit password", func() error {
			_, err := client.SubmitTelegramPassword(ctx, adminv1.TelegramPasswordRequest{LoginID: "l", Password: "p"})
			return err
		}, "POST", "/admin/v1/setup/telegram/password", "", `{"login_id":"l","password":"p"}`},
		{"cancel login", func() error { _, err := client.CancelTelegramLogin(ctx); return err }, "POST", "/admin/v1/setup/telegram/cancel", "", ""},
		{"unlink telegram", func() error { _, err := client.UnlinkTelegram(ctx); return err }, "POST", "/admin/v1/setup/telegram/unlink", "", `{"confirm":"unlink"}`},
		{"status", func() error { _, err := client.Status(ctx); return err }, "GET", "/admin/v1/status", "", ""},
		{"usage", func() error { _, err := client.Usage(ctx); return err }, "GET", "/admin/v1/usage", "", ""},
		{"list buckets", func() error { _, err := client.ListBuckets(ctx); return err }, "GET", "/admin/v1/buckets", "", ""},
		{"create bucket", func() error {
			_, err := client.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "photos", CacheMaxAgeSeconds: &one})
			return err
		}, "POST", "/admin/v1/buckets", "", `{"name":"photos","cache_max_age_seconds":3600}`},
		{"get bucket", func() error { _, err := client.GetBucket(ctx, "photos"); return err }, "GET", "/admin/v1/buckets/photos", "", ""},
		{"update bucket", func() error {
			policy := "private"
			_, err := client.UpdateBucket(ctx, "photos", adminv1.UpdateBucketRequest{Policy: &policy})
			return err
		}, "PATCH", "/admin/v1/buckets/photos", "", `{"policy":"private"}`},
		{"delete bucket", func() error { return client.DeleteBucket(ctx, "photos") }, "DELETE", "/admin/v1/buckets/photos", "", ""},
		{"list keys", func() error { _, err := client.ListAccessKeys(ctx, false); return err }, "GET", "/admin/v1/access-keys", "", ""},
		{"list keys revoked", func() error { _, err := client.ListAccessKeys(ctx, true); return err }, "GET", "/admin/v1/access-keys", "include_revoked=true", ""},
		{"create key", func() error {
			_, err := client.CreateAccessKey(ctx, adminv1.CreateAccessKeyRequest{Name: "k", Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read"}}})
			return err
		}, "POST", "/admin/v1/access-keys", "", `{"name":"k","scope":{"buckets":["photos"],"actions":["read"]}}`},
		{"get key", func() error { _, err := client.GetAccessKey(ctx, "BDBK1"); return err }, "GET", "/admin/v1/access-keys/BDBK1", "", ""},
		{"update key", func() error {
			_, err := client.UpdateAccessKey(ctx, "BDBK1", adminv1.UpdateAccessKeyRequest{Name: &name})
			return err
		}, "PATCH", "/admin/v1/access-keys/BDBK1", "", `{"name":"renamed"}`},
		{"revoke key", func() error { _, err := client.RevokeAccessKey(ctx, "BDBK1"); return err }, "DELETE", "/admin/v1/access-keys/BDBK1", "", ""},
		{"audit empty", func() error { _, err := client.Audit(ctx, adminv1.AuditQuery{}); return err }, "GET", "/admin/v1/audit", "", ""},
		{"audit full", func() error {
			_, err := client.Audit(ctx, adminv1.AuditQuery{Cursor: "c", Limit: 50, Action: "bucket.", Since: since, Until: since.Add(time.Hour)})
			return err
		}, "GET", "/admin/v1/audit", "action=bucket.&cursor=c&limit=50&since=2026-11-01T09%3A30%3A00.000Z&until=2026-11-01T10%3A30%3A00.000Z", ""},
		{"list objects", func() error {
			_, err := client.ListObjects(ctx, "photos", adminv1.ListObjectsOptions{Prefix: "2026/ 11", Delimiter: "/", Cursor: "cur", Limit: 25})
			return err
		}, "GET", "/admin/v1/buckets/photos/objects", "cursor=cur&delimiter=%2F&limit=25&prefix=2026%2F+11", ""},
		{"list objects defaults", func() error { _, err := client.ListObjects(ctx, "photos", adminv1.ListObjectsOptions{}); return err }, "GET", "/admin/v1/buckets/photos/objects", "", ""},
		{"object meta", func() error { _, err := client.ObjectMeta(ctx, "photos", "a/b.jpg"); return err }, "GET", "/admin/v1/buckets/photos/object/meta", "key=a%2Fb.jpg", ""},
		{"delete object", func() error { return client.DeleteObject(ctx, "photos", "a?b%c/../d+e") }, "DELETE", "/admin/v1/buckets/photos/object", "key=a%3Fb%25c%2F..%2Fd%2Be", ""},
		{"delete objects", func() error {
			_, err := client.DeleteObjects(ctx, "photos", adminv1.DeleteObjectsRequest{Keys: []string{"a", "b"}})
			return err
		}, "POST", "/admin/v1/buckets/photos/objects/delete", "", `{"keys":["a","b"]}`},
		{"presign", func() error {
			_, err := client.Presign(ctx, "photos", adminv1.PresignRequest{Key: "a", AccessKeyID: "K", ExpiresInSeconds: 60})
			return err
		}, "POST", "/admin/v1/buckets/photos/presign", "", `{"key":"a","access_key_id":"K","expires_in_seconds":60}`},
		{"replica status", func() error { _, err := client.ReplicaStatus(ctx); return err }, "GET", "/admin/v1/replica", "", ""},
		{"start replica login", func() error {
			_, err := client.StartReplicaLogin(ctx, adminv1.ReplicaLoginStartRequest{Phone: "+1", APIID: 2, APIHash: "h"})
			return err
		}, "POST", "/admin/v1/replica/telegram/phone", "", `{"phone":"+1","api_id":2,"api_hash":"h"}`},
		{"replica code", func() error {
			_, err := client.SubmitReplicaCode(ctx, adminv1.TelegramCodeRequest{LoginID: "l", Code: "1"})
			return err
		}, "POST", "/admin/v1/replica/telegram/code", "", `{"login_id":"l","code":"1"}`},
		{"replica password", func() error {
			_, err := client.SubmitReplicaPassword(ctx, adminv1.TelegramPasswordRequest{LoginID: "l", Password: "p"})
			return err
		}, "POST", "/admin/v1/replica/telegram/password", "", `{"login_id":"l","password":"p"}`},
		{"replica cancel", func() error { _, err := client.CancelReplicaLogin(ctx); return err }, "POST", "/admin/v1/replica/telegram/cancel", "", ""},
		{"replica unlink", func() error { _, err := client.UnlinkReplica(ctx); return err }, "POST", "/admin/v1/replica/unlink", "", `{"confirm":"unlink"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatalf("call: %v", err)
			}
			got := rec.last()
			if got.method != tc.wantMethod || got.path != tc.wantPath || got.query != tc.wantQuery {
				t.Fatalf("got %s %s?%s, want %s %s?%s", got.method, got.path, got.query, tc.wantMethod, tc.wantPath, tc.wantQuery)
			}
			if got.body != tc.wantBody {
				t.Fatalf("body = %q, want %q", got.body, tc.wantBody)
			}
			if tc.wantBody != "" && got.header.Get("Content-Type") != "application/json" {
				t.Fatalf("content type = %q", got.header.Get("Content-Type"))
			}
			if got.header.Get("User-Agent") == "" || !strings.HasPrefix(got.header.Get("User-Agent"), "butterdb-adminv1/") {
				t.Fatalf("user agent = %q", got.header.Get("User-Agent"))
			}
		})
	}
}

func TestResponseDecoding(t *testing.T) {
	client, _ := newPlainClient(t, okJSON(`{"device":{"device_id":"d1","name":"n","current":true},"device_token":"bdt1_x","server_id":"s","unknown_field":1}`), "")
	creds, err := client.RotateDeviceToken(context.Background())
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if creds.Device.DeviceID != "d1" || creds.DeviceToken != "bdt1_x" || !creds.Device.Current {
		t.Fatalf("creds = %+v", creds)
	}
	broken, _ := newPlainClient(t, okJSON(`{not json`), "")
	if _, err := broken.Status(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestEmptyIdentifiersRejected(t *testing.T) {
	client, rec := newPlainClient(t, okJSON(`{}`), "tok")
	ctx := context.Background()
	for name, run := range map[string]func() error{
		"bucket":     func() error { _, err := client.GetBucket(ctx, ""); return err },
		"device":     func() error { return client.RevokeDevice(ctx, "", false) },
		"access key": func() error { _, err := client.GetAccessKey(ctx, ""); return err },
		"object key": func() error { _, err := client.ObjectMeta(ctx, "b", ""); return err },
		"list":       func() error { _, err := client.ListObjects(ctx, "", adminv1.ListObjectsOptions{}); return err },
	} {
		if err := run(); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) != 0 {
		t.Fatalf("server saw %d calls", len(rec.calls))
	}
}

func TestGetObjectStatuses(t *testing.T) {
	cases := []struct {
		name        string
		opts        adminv1.GetObjectOptions
		wantRange   string
		wantStatus  int
		respond     func(w http.ResponseWriter)
		wantLength  int64
		wantSize    int64
		wantContent string
		wantErrCode adminv1.Code
	}{
		{
			name: "full body", wantStatus: 200, wantLength: 5, wantSize: 5, wantContent: "hello",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Length", "5")
				w.Header().Set(adminv1.HeaderVersionID, "9")
				_, _ = w.Write([]byte("hello"))
			},
		},
		{
			name: "closed range", opts: adminv1.GetObjectOptions{RangeStart: 1, RangeEnd: 3}, wantRange: "bytes=1-3", wantStatus: 206, wantLength: 3, wantSize: 5, wantContent: "ell",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Range", "bytes 1-3/5")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("ell"))
			},
		},
		{
			name: "open range", opts: adminv1.GetObjectOptions{RangeStart: 2}, wantRange: "bytes=2-", wantStatus: 206, wantLength: 3, wantSize: 5, wantContent: "llo",
			respond: func(w http.ResponseWriter) {
				w.Header().Set("Content-Range", "bytes 2-4/5")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("llo"))
			},
		},
		{
			name: "not modified", opts: adminv1.GetObjectOptions{IfNoneMatch: `"abc"`}, wantStatus: 304,
			respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotModified) },
		},
		{
			name: "range not satisfiable", opts: adminv1.GetObjectOptions{RangeStart: 99, RangeEnd: 100}, wantRange: "bytes=99-100", wantErrCode: adminv1.CodeRangeNotSatisfiable,
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				_, _ = w.Write([]byte(`{"error":{"code":"range_not_satisfiable","message":"no"}}`))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, rec := newPlainClient(t, func(w http.ResponseWriter, _ *http.Request) { tc.respond(w) }, "tok")
			stream, err := client.GetObject(context.Background(), "photos", "k", tc.opts)
			call := rec.last()
			if call.header.Get("Range") != tc.wantRange {
				t.Fatalf("range header = %q", call.header.Get("Range"))
			}
			if tc.opts.IfNoneMatch != "" && call.header.Get("If-None-Match") != tc.opts.IfNoneMatch {
				t.Fatalf("if-none-match = %q", call.header.Get("If-None-Match"))
			}
			if tc.wantErrCode != "" {
				if !adminv1.IsCode(err, tc.wantErrCode) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer stream.Body.Close()
			body, _ := io.ReadAll(stream.Body)
			if stream.Status != tc.wantStatus || string(body) != tc.wantContent {
				t.Fatalf("status = %d body = %q", stream.Status, body)
			}
			if tc.wantStatus != 304 && (stream.ContentLength != tc.wantLength || stream.Meta.SizeBytes != tc.wantSize) {
				t.Fatalf("length = %d size = %d", stream.ContentLength, stream.Meta.SizeBytes)
			}
			if stream.Meta.Bucket != "photos" || stream.Meta.Key != "k" {
				t.Fatalf("meta = %+v", stream.Meta)
			}
		})
	}
}

func TestGetObjectRangeValidation(t *testing.T) {
	client, rec := newPlainClient(t, okJSON(`{}`), "tok")
	for name, opts := range map[string]adminv1.GetObjectOptions{
		"negative start": {RangeStart: -1},
		"negative end":   {RangeEnd: -1},
		"end before":     {RangeStart: 10, RangeEnd: 5},
	} {
		if _, err := client.GetObject(context.Background(), "b", "k", opts); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) != 0 {
		t.Fatalf("server saw %d calls", len(rec.calls))
	}
}

func TestJSONTimeoutAppliesToJSONOnly(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/object") {
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte("done"))
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}
	server := httptest.NewServer(http.HandlerFunc(slow))
	t.Cleanup(server.Close)
	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, JSONTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("status error = %v, want deadline exceeded", err)
	}
	stream, err := client.GetObject(context.Background(), "b", "k", adminv1.GetObjectOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer stream.Body.Close()
	body, err := io.ReadAll(stream.Body)
	if err != nil || string(body) != "done" {
		t.Fatalf("body = %q err = %v", body, err)
	}
	if _, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, JSONTimeout: -time.Second}); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestCallerContextCancelsStreaming(t *testing.T) {
	block := make(chan struct{})
	client, _ := newPlainClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}, "tok")
	t.Cleanup(func() { close(block) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.GetObject(ctx, "b", "k", adminv1.GetObjectOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestRedirectsNotFollowed(t *testing.T) {
	var followed bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	t.Cleanup(other.Close)
	client, _ := newPlainClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}, "tok")
	_, err := client.Status(context.Background())
	var apiErr *adminv1.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("error = %v", err)
	}
	if followed {
		t.Fatal("redirect was followed")
	}
}

func TestCustomUserAgentAndTransport(t *testing.T) {
	var gotAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent = r.Header.Get("User-Agent")
		okJSON(`{}`)(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, UserAgent: "gui/1", HTTPTransport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAgent != "gui/1" {
		t.Fatalf("user agent = %q", gotAgent)
	}
}

func TestBasePathPrefixPreserved(t *testing.T) {
	client, rec := newPlainClientAt(t, "/prefix/", okJSON(`{}`))
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rec.last().path; got != "/prefix/admin/v1/status" {
		t.Fatalf("path = %q", got)
	}
}

func newPlainClientAt(t *testing.T, suffix string, handler http.HandlerFunc) (*adminv1.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.calls = append(rec.calls, recorded{method: r.Method, path: r.URL.EscapedPath(), header: r.Header.Clone()})
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL + suffix})
	if err != nil {
		t.Fatal(err)
	}
	return client, rec
}
