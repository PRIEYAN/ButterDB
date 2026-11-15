package router

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type fakeAuthenticator struct {
	auth sigv4.Auth
	err  error
}

func (f fakeAuthenticator) Authenticate(context.Context, *http.Request) (sigv4.Auth, error) {
	return f.auth, f.err
}

type authorizeCall struct {
	principal policy.Principal
	bucket    bucket.BucketName
	action    access.Action
	anonymous bool
}

type fakeAuthorizer struct {
	mu    sync.Mutex
	calls []authorizeCall
	err   error
}

func (f *fakeAuthorizer) Authorize(_ context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, authorizeCall{principal: p, bucket: b, action: a})
	return bucket.Info{Name: b}, f.err
}

func (f *fakeAuthorizer) AuthorizeAnonymousRead(_ context.Context, b bucket.BucketName) (bucket.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, authorizeCall{bucket: b, anonymous: true})
	return bucket.Info{Name: b}, f.err
}

type fakeReadiness struct{ state ServerState }

func (f fakeReadiness) State() ServerState { return f.state }

type counterReader struct{ next byte }

func (c *counterReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next++
	}
	return len(p), nil
}

type harness struct {
	router     *Router
	authorizer *fakeAuthorizer
	handled    []Route
}

type harnessOptions struct {
	auth     sigv4.Auth
	authErr  error
	authzErr error
	state    ServerState
	cors     CORSConfig
	addr     Addressing
	noHandle bool
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	h := &harness{authorizer: &fakeAuthorizer{err: opts.authzErr}}
	record := func(w http.ResponseWriter, _ *http.Request, rt Route, _ sigv4.Auth) {
		h.handled = append(h.handled, rt)
		w.Header().Set("ETag", `"x"`)
		w.WriteHeader(http.StatusOK)
	}
	handlers := map[Operation]HandlerFunc{}
	if !opts.noHandle {
		for op := range Actions() {
			handlers[op] = record
		}
		handlers[OpListBuckets] = record
		handlers[OpCreateBucket] = record
		handlers[OpDeleteBucket] = record
	}
	rt, err := NewRouter(Dependencies{
		Addressing:    opts.addr,
		Authenticator: fakeAuthenticator{auth: opts.auth, err: opts.authErr},
		Authorizer:    h.authorizer,
		Readiness:     fakeReadiness{state: opts.state},
		CORS:          opts.cors,
		Handlers:      handlers,
		Random:        &counterReader{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	h.router = rt
	return h
}

func (h *harness) serve(method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := newRequest(method, "localhost:9000", target)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, r)
	return rec
}

func signedAuth() sigv4.Auth {
	scope, err := access.NewScope([]string{"photos"}, []string{"read", "write"})
	if err != nil {
		panic(err)
	}
	return sigv4.Auth{Kind: sigv4.AuthHeader, Key: access.KeyInfo{ID: "BDBKEXAMPLE", Scope: scope}}
}

func TestRouterDispatchesSignedRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{auth: signedAuth()})
	rec := h.serve(http.MethodGet, "/photos/cat.jpg", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(h.handled) != 1 || h.handled[0].Operation != OpGetObject || h.handled[0].Target.Key != "cat.jpg" {
		t.Fatalf("handled = %+v", h.handled)
	}
	if len(h.authorizer.calls) != 1 {
		t.Fatalf("authorizer calls = %+v", h.authorizer.calls)
	}
	call := h.authorizer.calls[0]
	if call.bucket != "photos" || call.action != access.ActionRead || call.anonymous || call.principal.Key.ID != "BDBKEXAMPLE" {
		t.Fatalf("authorizer call = %+v", call)
	}
	if rec.Header().Get("X-Amz-Request-Id") == "" || rec.Header().Get("X-Amz-Id-2") == "" {
		t.Fatalf("response ids missing: %v", rec.Header())
	}
}

func TestRouterAuthorizationOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		opts       func(t *testing.T) harnessOptions
		method     string
		target     string
		wantStatus int
		wantCode   string
		wantKey    string
		wantBucket string
		handled    bool
	}{
		{"anonymous public read object", func(t *testing.T) harnessOptions { return harnessOptions{} }, "GET", "/photos/a", 200, "", "", "", true},
		{"anonymous head object", func(t *testing.T) harnessOptions { return harnessOptions{} }, "HEAD", "/photos/a", 200, "", "", "", true},
		{"anonymous list denied without lookup", func(t *testing.T) harnessOptions { return harnessOptions{} }, "GET", "/photos", 403, "AccessDenied", "", "", false},
		{"anonymous put denied", func(t *testing.T) harnessOptions { return harnessOptions{} }, "PUT", "/photos/a", 403, "AccessDenied", "", "", false},
		{"anonymous list buckets denied", func(t *testing.T) harnessOptions { return harnessOptions{} }, "GET", "/", 403, "AccessDenied", "", "", false},
		{"anonymous create bucket denied", func(t *testing.T) harnessOptions { return harnessOptions{} }, "PUT", "/photos", 403, "AccessDenied", "", "", false},
		{"anonymous private bucket denied", func(t *testing.T) harnessOptions { return harnessOptions{authzErr: errs.ErrAccessDenied} }, "GET", "/photos/a", 403, "AccessDenied", "", "", false},
		{"signed out of scope", func(t *testing.T) harnessOptions {
			return harnessOptions{auth: signedAuth(), authzErr: errs.ErrAccessDenied}
		}, "GET", "/other/a", 403, "AccessDenied", "", "", false},
		{"signed missing bucket", func(t *testing.T) harnessOptions {
			return harnessOptions{auth: signedAuth(), authzErr: errs.ErrNoSuchBucket}
		}, "GET", "/photos/a", 404, "NoSuchBucket", "", "photos", false},
		{"signed list buckets skips bucket check", func(t *testing.T) harnessOptions { return harnessOptions{auth: signedAuth()} }, "GET", "/", 200, "", "", "", true},
		{"signed create bucket reaches handler", func(t *testing.T) harnessOptions { return harnessOptions{auth: signedAuth()} }, "PUT", "/photos", 200, "", "", "", true},
		{"authentication failure", func(t *testing.T) harnessOptions {
			return harnessOptions{authErr: errs.ErrUnknownAccessKey}
		}, "GET", "/photos/a", 403, "InvalidAccessKeyId", "", "", false},
		{"signature failure", func(t *testing.T) harnessOptions {
			return harnessOptions{authErr: errs.ErrSignatureMismatch}
		}, "GET", "/photos/a", 403, "SignatureDoesNotMatch", "", "", false},
		{"unknown operation is 501", func(t *testing.T) harnessOptions { return harnessOptions{auth: signedAuth()} }, "GET", "/photos/a?attributes", 501, "NotImplemented", "", "", false},
		{"wrong method is 405 before authentication", func(t *testing.T) harnessOptions {
			return harnessOptions{authErr: errs.ErrSignatureMismatch}
		}, "POST", "/photos/a", 405, "MethodNotAllowed", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts(t))
			rec := h.serve(tc.method, tc.target, nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if (len(h.handled) == 1) != tc.handled {
				t.Fatalf("handled = %+v, want handled %v", h.handled, tc.handled)
			}
			if tc.wantCode == "" {
				return
			}
			var doc struct {
				Code       string `xml:"Code"`
				BucketName string `xml:"BucketName"`
				RequestID  string `xml:"RequestId"`
			}
			if tc.method != http.MethodHead {
				if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
					t.Fatalf("body %q: %v", rec.Body.String(), err)
				}
				if doc.Code != tc.wantCode || doc.BucketName != tc.wantBucket {
					t.Fatalf("error document = %+v", doc)
				}
				if doc.RequestID != rec.Header().Get("X-Amz-Request-Id") {
					t.Fatalf("RequestId %q differs from the header %q", doc.RequestID, rec.Header().Get("X-Amz-Request-Id"))
				}
			}
		})
	}
}

func TestRouterAnonymousGetChecksPublicRead(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.serve(http.MethodGet, "/photos/a", nil)
	if len(h.authorizer.calls) != 1 || !h.authorizer.calls[0].anonymous || h.authorizer.calls[0].bucket != "photos" {
		t.Fatalf("authorizer calls = %+v", h.authorizer.calls)
	}
	h = newHarness(t, harnessOptions{})
	h.serve(http.MethodGet, "/photos", nil)
	if len(h.authorizer.calls) != 0 {
		t.Fatalf("anonymous list reached the authorizer: %+v", h.authorizer.calls)
	}
}

func TestRouterReadiness(t *testing.T) {
	cases := []struct {
		name       string
		state      ServerState
		method     string
		target     string
		wantStatus int
		wantCode   string
		retryAfter string
	}{
		{"ready write", StateReady, "PUT", "/photos/a", 200, "", ""},
		{"starting read", StateStarting, "GET", "/photos/a", 503, "ServiceUnavailable", "30"},
		{"starting write", StateStarting, "PUT", "/photos/a", 503, "ServiceUnavailable", "30"},
		{"degraded read served", StateDegraded, "GET", "/photos/a", 200, "", ""},
		{"degraded head served", StateDegraded, "HEAD", "/photos/a", 200, "", ""},
		{"degraded put", StateDegraded, "PUT", "/photos/a", 503, "SlowDown", "5"},
		{"degraded delete", StateDegraded, "DELETE", "/photos/a", 503, "SlowDown", "5"},
		{"degraded post", StateDegraded, "POST", "/photos?delete", 503, "SlowDown", "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{auth: signedAuth(), state: tc.state})
			rec := h.serve(tc.method, tc.target, nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retryAfter)
			}
			if tc.wantCode != "" && !strings.Contains(rec.Body.String(), "<Code>"+tc.wantCode+"</Code>") {
				t.Fatalf("body = %q", rec.Body.String())
			}
		})
	}
}

func TestRouterPreflightIgnoresReadinessAndAuth(t *testing.T) {
	cors := CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET", "PUT"}, AllowedHeaders: []string{"*"}, MaxAgeSeconds: 3600}
	h := newHarness(t, harnessOptions{state: StateStarting, authErr: errs.ErrSignatureMismatch, cors: cors})
	rec := h.serve(http.MethodOptions, "/photos/a", map[string]string{
		"Origin":                         "https://app.example.com",
		"Access-Control-Request-Method":  "PUT",
		"Access-Control-Request-Headers": "x-amz-meta-a, content-type",
	})
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(h.handled) != 0 || len(h.authorizer.calls) != 0 {
		t.Fatal("a preflight reached a handler or the authorizer")
	}
}

func TestRouterCORS(t *testing.T) {
	cors := CORSConfig{
		AllowedOrigins: []string{"https://app.example.com", "https://*.cdn.example.com"},
		AllowedMethods: []string{"GET", "HEAD", "PUT"},
		AllowedHeaders: []string{"Content-Type", "X-Amz-*"},
		MaxAgeSeconds:  600,
	}
	cases := []struct {
		name       string
		cors       CORSConfig
		headers    map[string]string
		wantStatus int
		wantCode   string
		wantAllow  string
		wantHdrs   string
	}{
		{"missing origin", cors, map[string]string{"Access-Control-Request-Method": "GET"}, 400, "BadRequest", "", ""},
		{"missing request method", cors, map[string]string{"Origin": "https://app.example.com"}, 400, "BadRequest", "", ""},
		{"allowed origin and method", cors, map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET"}, 200, "", "https://app.example.com", ""},
		{"origin host case insensitive", cors, map[string]string{"Origin": "https://APP.example.com", "Access-Control-Request-Method": "GET"}, 200, "", "https://APP.example.com", ""},
		{"wildcard origin pattern", cors, map[string]string{"Origin": "https://a.cdn.example.com", "Access-Control-Request-Method": "PUT"}, 200, "", "https://a.cdn.example.com", ""},
		{"wildcard pattern needs a label", cors, map[string]string{"Origin": "https://cdn.example.com", "Access-Control-Request-Method": "PUT"}, 403, "AccessForbidden", "", ""},
		{"origin not allowed", cors, map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "GET"}, 403, "AccessForbidden", "", ""},
		{"method not allowed", cors, map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "DELETE"}, 403, "AccessForbidden", "", ""},
		{"header not allowed", cors, map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}, 403, "AccessForbidden", "", ""},
		{"headers echoed", cors, map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "content-type"}, 200, "", "https://app.example.com", "content-type"},
		{"disabled refuses", CORSConfig{}, map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET"}, 403, "AccessForbidden", "", ""},
		{"star origin answers star", CORSConfig{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}, AllowedHeaders: []string{"*"}}, map[string]string{"Origin": "https://x.example", "Access-Control-Request-Method": "GET"}, 200, "", "*", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{cors: tc.cors})
			rec := h.serve(http.MethodOptions, "/photos/a", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" && !strings.Contains(rec.Body.String(), "<Code>"+tc.wantCode+"</Code>") {
				t.Fatalf("body = %q", rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantAllow)
			}
			if tc.wantStatus == 200 {
				if rec.Header().Get("Access-Control-Allow-Methods") == "" || rec.Header().Get("Vary") != preflightVary {
					t.Fatalf("preflight headers = %v", rec.Header())
				}
			}
			if tc.wantHdrs != "" && rec.Header().Get("Access-Control-Allow-Headers") != tc.wantHdrs {
				t.Fatalf("Access-Control-Allow-Headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
			}
		})
	}
}

func TestRouterCORSOnActualRequestsAndErrors(t *testing.T) {
	cors := CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET"}, AllowedHeaders: []string{"*"}}
	cases := []struct {
		name    string
		opts    harnessOptions
		target  string
		origin  string
		wantACO string
	}{
		{"success with allowed origin", harnessOptions{auth: signedAuth(), cors: cors}, "/photos/a", "https://app.example.com", "https://app.example.com"},
		{"error with allowed origin", harnessOptions{authErr: errs.ErrSignatureMismatch, cors: cors}, "/photos/a", "https://app.example.com", "https://app.example.com"},
		{"origin not allowed", harnessOptions{auth: signedAuth(), cors: cors}, "/photos/a", "https://evil.example", ""},
		{"no origin", harnessOptions{auth: signedAuth(), cors: cors}, "/photos/a", "", ""},
		{"cors disabled", harnessOptions{auth: signedAuth()}, "/photos/a", "https://app.example.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts)
			headers := map[string]string{}
			if tc.origin != "" {
				headers["Origin"] = tc.origin
			}
			rec := h.serve(http.MethodGet, tc.target, headers)
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantACO {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantACO)
			}
			if tc.wantACO != "" {
				if rec.Header().Get("Access-Control-Expose-Headers") == "" || !strings.Contains(rec.Header().Get("Vary"), "Origin") {
					t.Fatalf("headers = %v", rec.Header())
				}
				if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
					t.Fatal("credentials header must never be sent")
				}
			}
		})
	}
}

func TestNosniffOnEveryResponse(t *testing.T) {
	cors := CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET"}, AllowedHeaders: []string{"*"}}
	preflight := map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET"}
	cases := []struct {
		name    string
		opts    harnessOptions
		method  string
		target  string
		headers map[string]string
	}{
		{"success", harnessOptions{auth: signedAuth()}, "GET", "/photos/a", nil},
		{"handler error", harnessOptions{auth: signedAuth(), authzErr: errs.ErrAccessDenied}, "GET", "/photos/a", nil},
		{"authentication error", harnessOptions{authErr: errs.ErrSignatureMismatch}, "GET", "/photos/a", nil},
		{"invalid bucket name", harnessOptions{}, "GET", "/UPPER/a", nil},
		{"method not allowed", harnessOptions{}, "PATCH", "/photos/a", nil},
		{"not implemented", harnessOptions{auth: signedAuth()}, "GET", "/photos?versions", nil},
		{"starting", harnessOptions{state: StateStarting}, "GET", "/photos/a", nil},
		{"degraded write", harnessOptions{state: StateDegraded}, "PUT", "/photos/a", nil},
		{"allowed preflight", harnessOptions{cors: cors}, "OPTIONS", "/photos/a", preflight},
		{"refused preflight", harnessOptions{}, "OPTIONS", "/photos/a", preflight},
		{"missing handler", harnessOptions{auth: signedAuth(), noHandle: true}, "GET", "/photos/a", nil},
		{"head error", harnessOptions{authzErr: errs.ErrAccessDenied}, "HEAD", "/photos/a", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts)
			rec := h.serve(tc.method, tc.target, tc.headers)
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q (status %d)", got, rec.Code)
			}
		})
	}
}

func TestRouterHeadErrorHasNoBody(t *testing.T) {
	h := newHarness(t, harnessOptions{auth: signedAuth(), authzErr: errs.ErrNoSuchBucket})
	rec := h.serve(http.MethodHead, "/photos", nil)
	if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestNewRouterRejectsNilDependencies(t *testing.T) {
	valid := func() Dependencies {
		return Dependencies{
			Authenticator: fakeAuthenticator{},
			Authorizer:    &fakeAuthorizer{},
			Readiness:     fakeReadiness{},
			Handlers:      map[Operation]HandlerFunc{},
			Random:        &counterReader{},
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}
	cases := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{"authenticator", func(d *Dependencies) { d.Authenticator = nil }},
		{"authorizer", func(d *Dependencies) { d.Authorizer = nil }},
		{"readiness", func(d *Dependencies) { d.Readiness = nil }},
		{"handlers", func(d *Dependencies) { d.Handlers = nil }},
		{"random", func(d *Dependencies) { d.Random = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
		{"nil handler entry", func(d *Dependencies) { d.Handlers = map[Operation]HandlerFunc{OpGetObject: nil} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := valid()
			tc.mutate(&deps)
			if _, err := NewRouter(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewRouter error = %v", err)
			}
		})
	}
	if _, err := NewRouter(valid()); err != nil {
		t.Fatalf("NewRouter(valid): %v", err)
	}
}

func TestWriteErrorDescribesTarget(t *testing.T) {
	rec := httptest.NewRecorder()
	r := newRequest(http.MethodGet, "localhost", "/photos/a")
	WriteError(rec, r, Target{Bucket: "photos", Key: "a", HasKey: true}, errs.ErrNoSuchKey)
	body := rec.Body.String()
	for _, want := range []string{"<Key>a</Key>", "<BucketName>photos</BucketName>", "<Resource>/photos/a</Resource>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q lacks %s", body, want)
		}
	}
}
