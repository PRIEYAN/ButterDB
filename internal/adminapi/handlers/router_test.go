package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var hexRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestErrorEnvelopeShape(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   adminv1.Code
		wantAllow  string
		wantBody   bool
	}{
		{name: "unknown route", method: http.MethodGet, path: "/admin/v1/nothing", wantStatus: 404, wantCode: adminv1.CodeNotFound, wantBody: true},
		{name: "outside base path", method: http.MethodGet, path: "/", wantStatus: 404, wantCode: adminv1.CodeNotFound, wantBody: true},
		{name: "method not allowed", method: http.MethodPost, path: adminv1.PathReplica, wantStatus: 405, wantCode: adminv1.CodeMethodNotAllowed, wantAllow: "GET, HEAD", wantBody: true},
		{name: "head error has no body", method: http.MethodHead, path: "/admin/v1/nothing", wantStatus: 404, wantCode: adminv1.CodeNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: test.method, path: test.path})
			if w.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, test.wantStatus)
			}
			if got := w.Header().Get(adminv1.HeaderErrorCode); got != string(test.wantCode) {
				t.Fatalf("error code header = %q, want %q", got, test.wantCode)
			}
			if got := w.Header().Get("Allow"); got != test.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, test.wantAllow)
			}
			id := w.Header().Get(adminv1.HeaderRequestID)
			if !hexRequestID.MatchString(id) {
				t.Fatalf("request id %q is not 32 hex characters", id)
			}
			if !test.wantBody {
				if w.Body.Len() != 0 {
					t.Fatalf("HEAD error carried body %q", w.Body.String())
				}
				return
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", ct)
			}
			body := decodeError(t, w)
			if body.Code != test.wantCode || body.RequestID != id || body.Message == "" {
				t.Fatalf("body = %+v, want code %q request id %q", body, test.wantCode, id)
			}
			raw := w.Body.String()
			for _, field := range []string{`"error"`, `"code"`, `"message"`, `"retryable"`, `"hint"`, `"request_id"`} {
				if !strings.Contains(raw, field) {
					t.Fatalf("envelope %s lacks %s", raw, field)
				}
			}
		})
	}
}

func TestRequestIDsAreDistinct(t *testing.T) {
	h := newHarness(t)
	first := h.do(request{method: http.MethodGet, path: "/admin/v1/nothing"}).Header().Get(adminv1.HeaderRequestID)
	second := h.do(request{method: http.MethodGet, path: "/admin/v1/nothing"}).Header().Get(adminv1.HeaderRequestID)
	if first == second {
		t.Fatalf("request ids repeat: %q", first)
	}
}

func TestHostCheck(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		host    string
		want    int
	}{
		{name: "no allow list accepts anything", allowed: nil, host: "evil.example", want: 404},
		{name: "localhost with port", allowed: []string{"localhost", "127.0.0.1"}, host: "localhost:9443", want: 404},
		{name: "loopback ipv4", allowed: []string{"localhost", "127.0.0.1"}, host: "127.0.0.1:9443", want: 404},
		{name: "loopback ipv6", allowed: []string{"::1"}, host: "[::1]:9443", want: 404},
		{name: "configured name is case insensitive", allowed: []string{"Admin.Example.com"}, host: "ADMIN.example.com", want: 404},
		{name: "rebinding host", allowed: []string{"localhost"}, host: "attacker.example:9443", want: 421},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.config.AllowedHosts = test.allowed
			h.build()
			r := httptest.NewRequest(http.MethodGet, "/admin/v1/nothing", nil)
			r.Host = test.host
			w := httptest.NewRecorder()
			h.handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status = %d, want %d", w.Code, test.want)
			}
			if test.want == 421 {
				assertError(t, w, 421, adminv1.CodeMisdirectedRequest)
			}
		})
	}
}

func TestReplicaRoutes501BeforePhase12(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, adminv1.PathReplica},
		{http.MethodPost, adminv1.PathReplicaTelegramPhone},
		{http.MethodPost, adminv1.PathReplicaTelegramCode},
		{http.MethodPost, adminv1.PathReplicaTelegramPass},
		{http.MethodPost, adminv1.PathReplicaTelegramCancel},
		{http.MethodPost, adminv1.PathReplicaUnlink},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: route.method, path: route.path})
			body := assertError(t, w, 501, adminv1.CodeNotImplemented)
			if phase, ok := body.Details["phase"].(float64); !ok || phase != 12 {
				t.Fatalf("details = %v, want phase 12", body.Details)
			}
		})
	}
}

func TestNewRouterRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{name: "buckets", mutate: func(d *Dependencies) { d.Buckets = nil }},
		{name: "keys", mutate: func(d *Dependencies) { d.Keys = nil }},
		{name: "lister", mutate: func(d *Dependencies) { d.Lister = nil }},
		{name: "writer", mutate: func(d *Dependencies) { d.Writer = nil }},
		{name: "server", mutate: func(d *Dependencies) { d.Server = nil }},
		{name: "presign", mutate: func(d *Dependencies) { d.Presign = nil }},
		{name: "status", mutate: func(d *Dependencies) { d.Status = nil }},
		{name: "audit", mutate: func(d *Dependencies) { d.Audit = nil }},
		{name: "setup", mutate: func(d *Dependencies) { d.Setup = nil }},
		{name: "pairing", mutate: func(d *Dependencies) { d.Pairing = nil }},
		{name: "devices", mutate: func(d *Dependencies) { d.Devices = nil }},
		{name: "device resolver", mutate: func(d *Dependencies) { d.Device = nil }},
		{name: "auth", mutate: func(d *Dependencies) { d.Auth = nil }},
		{name: "clock", mutate: func(d *Dependencies) { d.Clock = nil }},
		{name: "random", mutate: func(d *Dependencies) { d.Random = nil }},
		{name: "logger", mutate: func(d *Dependencies) { d.Logger = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := newHarness(t).dependencies()
			test.mutate(&deps)
			if _, err := NewRouter(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewRouter() error = %v, want ErrMissingDependency", err)
			}
		})
	}
	t.Run("bad trusted proxy", func(t *testing.T) {
		deps := newHarness(t).dependencies()
		deps.Config.TrustedProxies = []string{"not-a-cidr"}
		if _, err := NewRouter(deps); err == nil {
			t.Fatal("NewRouter() accepted an invalid trusted proxy")
		}
	})
}

func TestClientAddress(t *testing.T) {
	tests := []struct {
		name    string
		remote  string
		forward []string
		trusted []string
		want    string
	}{
		{name: "direct peer", remote: "203.0.113.7:5000", want: "203.0.113.7"},
		{name: "untrusted peer header ignored", remote: "203.0.113.7:5000", forward: []string{"198.51.100.1"}, trusted: []string{"127.0.0.1/32"}, want: "203.0.113.7"},
		{name: "trusted peer uses forwarded", remote: "127.0.0.1:5000", forward: []string{"198.51.100.1"}, trusted: []string{"127.0.0.1/32"}, want: "198.51.100.1"},
		{name: "rightmost untrusted entry wins", remote: "127.0.0.1:5000", forward: []string{"1.1.1.1, 198.51.100.1, 127.0.0.1"}, trusted: []string{"127.0.0.1/32"}, want: "198.51.100.1"},
		{name: "garbage forwarded falls back to peer", remote: "127.0.0.1:5000", forward: []string{"garbage"}, trusted: []string{"127.0.0.1/32"}, want: "127.0.0.1"},
		{name: "ipv6 peer", remote: "[::1]:5000", trusted: []string{"::1/128"}, want: "::1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.config.TrustedProxies = test.trusted
			deps := h.dependencies()
			router, err := NewRouter(deps)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = test.remote
			for _, value := range test.forward {
				r.Header.Add("X-Forwarded-For", value)
			}
			if got := router.(*api).clientAddress(r); got != test.want {
				t.Fatalf("clientAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func allCodeExpectations() map[adminv1.Code]struct {
	status    int
	retryable bool
} {
	type row = struct {
		status    int
		retryable bool
	}
	return map[adminv1.Code]row{
		adminv1.CodeBadRequest:                   {400, false},
		adminv1.CodeValidationFailed:             {422, false},
		adminv1.CodeUnauthenticated:              {401, false},
		adminv1.CodeDeviceRevoked:                {401, false},
		adminv1.CodeNotFound:                     {404, false},
		adminv1.CodeMethodNotAllowed:             {405, false},
		adminv1.CodeMisdirectedRequest:           {421, false},
		adminv1.CodeLengthRequired:               {411, false},
		adminv1.CodePayloadTooLarge:              {413, false},
		adminv1.CodeUnsupportedMediaType:         {415, false},
		adminv1.CodeRateLimited:                  {429, true},
		adminv1.CodeInternal:                     {500, false},
		adminv1.CodeServerNotReady:               {503, true},
		adminv1.CodeNotImplemented:               {501, false},
		adminv1.CodeAlreadyClaimed:               {409, false},
		adminv1.CodeNotClaimed:                   {409, false},
		adminv1.CodeSetupTokenInvalid:            {401, false},
		adminv1.CodePairingCodeInvalid:           {401, false},
		adminv1.CodePairingUnavailable:           {409, false},
		adminv1.CodeLastDevice:                   {409, false},
		adminv1.CodeDeviceNotFound:               {404, false},
		adminv1.CodeTelegramNotLinked:            {409, false},
		adminv1.CodeTelegramUnavailable:          {503, true},
		adminv1.CodeTelegramFloodWait:            {503, true},
		adminv1.CodeTelegramCredentialsMissing:   {409, false},
		adminv1.CodeTelegramAlreadyLinked:        {409, false},
		adminv1.CodeTelegramLoginInProgress:      {409, false},
		adminv1.CodeTelegramLoginNotActive:       {409, false},
		adminv1.CodeTelegramAPIIDInvalid:         {422, false},
		adminv1.CodeTelegramPhoneInvalid:         {422, false},
		adminv1.CodeTelegramPhoneBanned:          {403, false},
		adminv1.CodeTelegramAccountNotRegistered: {422, false},
		adminv1.CodeTelegramCodeInvalid:          {422, true},
		adminv1.CodeTelegramCodeExpired:          {410, false},
		adminv1.CodeTelegramPasswordInvalid:      {422, true},
		adminv1.CodeTelegramAttemptsExhausted:    {409, false},
		adminv1.CodeTelegramEmailSetupRequired:   {422, false},
		adminv1.CodeTelegramAccountMismatch:      {409, false},
		adminv1.CodeTelegramLoginFailed:          {502, false},
		adminv1.CodeBucketNotFound:               {404, false},
		adminv1.CodeBucketExists:                 {409, false},
		adminv1.CodeBucketNameInvalid:            {422, false},
		adminv1.CodeBucketNotEmpty:               {409, false},
		adminv1.CodeBucketLimitReached:           {409, false},
		adminv1.CodeAccessKeyNotFound:            {404, false},
		adminv1.CodeScopeInvalid:                 {422, false},
		adminv1.CodeObjectNotFound:               {404, false},
		adminv1.CodeObjectKeyInvalid:             {422, false},
		adminv1.CodeObjectTooLarge:               {413, false},
		adminv1.CodeMetadataTooLarge:             {422, false},
		adminv1.CodeChecksumMismatch:             {400, false},
		adminv1.CodePreconditionFailed:           {412, false},
		adminv1.CodeRangeNotSatisfiable:          {416, false},
		adminv1.CodeIdempotencyConflict:          {409, false},
		adminv1.CodeCommitOutcomeUnknown:         {503, true},
		adminv1.CodePresignKeyInvalid:            {422, false},
		adminv1.CodeS3PublicURLUnset:             {409, false},
		adminv1.CodeReplicaNotLinked:             {409, false},
		adminv1.CodeReplicaAlreadyLinked:         {409, false},
		adminv1.CodeReplicaSameAccount:           {409, false},
	}
}

func TestEveryCodeMapped(t *testing.T) {
	t.Run("code table matches the spec table", func(t *testing.T) {
		want := allCodeExpectations()
		seen := map[adminv1.Code]bool{}
		for _, info := range codeTable() {
			expect, ok := want[info.code]
			if !ok {
				t.Fatalf("code %q is not in the spec table", info.code)
			}
			if seen[info.code] {
				t.Fatalf("code %q is listed twice", info.code)
			}
			seen[info.code] = true
			if info.status != expect.status || info.retryable != expect.retryable {
				t.Errorf("code %q = %d retryable %v, want %d retryable %v", info.code, info.status, info.retryable, expect.status, expect.retryable)
			}
			if info.message == "" {
				t.Errorf("code %q has no message", info.code)
			}
		}
		for code := range want {
			if !seen[code] {
				t.Errorf("code %q has no table row", code)
			}
		}
		if len(want) != 60 {
			t.Fatalf("spec table has %d codes, test expects 60", len(want))
		}
	})

	t.Run("every errs sentinel maps", func(t *testing.T) {
		sentinels := map[error]adminv1.Code{
			errs.ErrNoSuchBucket:       adminv1.CodeBucketNotFound,
			errs.ErrNoSuchKey:          adminv1.CodeObjectNotFound,
			errs.ErrBucketExists:       adminv1.CodeBucketExists,
			errs.ErrBucketNotEmpty:     adminv1.CodeBucketNotEmpty,
			errs.ErrInvalidBucketName:  adminv1.CodeBucketNameInvalid,
			errs.ErrInvalidKey:         adminv1.CodeObjectKeyInvalid,
			errs.ErrObjectTooLarge:     adminv1.CodeObjectTooLarge,
			errs.ErrMetadataTooLarge:   adminv1.CodeMetadataTooLarge,
			errs.ErrEmptyFile:          adminv1.CodeValidationFailed,
			errs.ErrAccessDenied:       adminv1.CodeUnauthenticated,
			errs.ErrUnknownAccessKey:   adminv1.CodeAccessKeyNotFound,
			errs.ErrAccessKeyNotFound:  adminv1.CodeAccessKeyNotFound,
			errs.ErrAccessKeyRevoked:   adminv1.CodeAccessKeyNotFound,
			errs.ErrSignatureMismatch:  adminv1.CodeInternal,
			errs.ErrRequestExpired:     adminv1.CodeInternal,
			errs.ErrPreconditionFailed: adminv1.CodePreconditionFailed,
			errs.ErrInvalidRange:       adminv1.CodeRangeNotSatisfiable,
			errs.ErrBadDigest:          adminv1.CodeChecksumMismatch,
			errs.ErrNoSuchUpload:       adminv1.CodeInternal,
			errs.ErrInvalidPart:        adminv1.CodeInternal,
			errs.ErrInvalidPartOrder:   adminv1.CodeInternal,
			errs.ErrEntityTooSmall:     adminv1.CodeInternal,
			errs.ErrUploadBusy:         adminv1.CodeInternal,
			errs.ErrSlowDown:           adminv1.CodeTelegramFloodWait,
			errs.ErrServiceUnavailable: adminv1.CodeTelegramUnavailable,
			errs.ErrNotImplemented:     adminv1.CodeNotImplemented,
			errs.ErrKeyUnavailable:     adminv1.CodeInternal,
			errs.ErrMissingDependency:  adminv1.CodeInternal,
		}
		if len(errsCodes()) != len(sentinels) {
			t.Fatalf("errsCodes has %d entries, test lists %d sentinels", len(errsCodes()), len(sentinels))
		}
		for sentinel, want := range sentinels {
			wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sentinel))
			got := MapError(wrapped)
			if got.Body.Code != want {
				t.Errorf("MapError(%v) = %q, want %q", sentinel, got.Body.Code, want)
			}
			if got.Status != lookupCode(want).status {
				t.Errorf("MapError(%v) status = %d", sentinel, got.Status)
			}
		}
	})

	t.Run("package sentinels", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
			want adminv1.Code
		}{
			{"nil", nil, adminv1.CodeInternal},
			{"unknown", errors.New("boom"), adminv1.CodeInternal},
			{"commit outcome unknown", put.ErrCommitOutcomeUnknown, adminv1.CodeCommitOutcomeUnknown},
			{"send outcome unknown", tgclient.ErrSendOutcomeUnknown, adminv1.CodeCommitOutcomeUnknown},
			{"control projection", put.ErrControlProjection, adminv1.CodeCommitOutcomeUnknown},
			{"idempotency conflict", put.ErrIdempotencyConflict, adminv1.CodeIdempotencyConflict},
			{"put invalid request", put.ErrInvalidRequest, adminv1.CodeValidationFailed},
			{"length mismatch", spool.ErrLengthMismatch, adminv1.CodeBadRequest},
			{"flood wait value", &tgclient.FloodWaitError{Duration: 3 * time.Second}, adminv1.CodeTelegramFloodWait},
			{"flood wait sentinel", tgclient.ErrFloodWait, adminv1.CodeTelegramFloodWait},
			{"not authorized", tgclient.ErrNotAuthorized, adminv1.CodeTelegramNotLinked},
			{"channel unavailable", tgclient.ErrChannelUnavailable, adminv1.CodeTelegramUnavailable},
			{"slow down", &admission.SlowDownError{RetryAfter: time.Second}, adminv1.CodeTelegramFloodWait},
			{"unsatisfiable range", &httprange.UnsatisfiableRangeError{Requested: "bytes=9-", Size: 5}, adminv1.CodeRangeNotSatisfiable},
			{"precondition", &httprange.PreconditionError{Condition: "If-Match"}, adminv1.CodePreconditionFailed},
			{"bucket invalid name", bucket.ErrInvalidBucketName, adminv1.CodeBucketNameInvalid},
			{"bucket invalid policy", bucket.ErrInvalidPolicy, adminv1.CodeValidationFailed},
			{"bucket invalid encryption", bucket.ErrInvalidEncryption, adminv1.CodeValidationFailed},
			{"bucket invalid cache age", bucket.ErrInvalidCacheMaxAge, adminv1.CodeValidationFailed},
			{"scope invalid", access.ErrInvalidScope, adminv1.CodeScopeInvalid},
			{"action invalid", access.ErrInvalidAction, adminv1.CodeScopeInvalid},
			{"key id invalid", access.ErrInvalidAccessKeyID, adminv1.CodeAccessKeyNotFound},
			{"key name invalid", access.ErrInvalidKeyName, adminv1.CodeValidationFailed},
			{"object key invalid", object.ErrInvalidKey, adminv1.CodeObjectKeyInvalid},
			{"object metadata too large", object.ErrMetadataTooLarge, adminv1.CodeMetadataTooLarge},
			{"metadata name invalid", object.ErrInvalidMetadataName, adminv1.CodeValidationFailed},
			{"metadata text invalid", object.ErrInvalidMetadataText, adminv1.CodeValidationFailed},
			{"already claimed", ErrAlreadyClaimed, adminv1.CodeAlreadyClaimed},
			{"not claimed", ErrNotClaimed, adminv1.CodeNotClaimed},
			{"setup token invalid", ErrSetupTokenInvalid, adminv1.CodeSetupTokenInvalid},
			{"pairing code invalid", ErrPairingCodeInvalid, adminv1.CodePairingCodeInvalid},
			{"pairing unavailable", ErrPairingUnavailable, adminv1.CodePairingUnavailable},
			{"last device", ErrLastDevice, adminv1.CodeLastDevice},
			{"device not found", ErrDeviceNotFound, adminv1.CodeDeviceNotFound},
			{"telegram not linked", ErrTelegramNotLinked, adminv1.CodeTelegramNotLinked},
			{"server not ready", ErrServerNotReady, adminv1.CodeServerNotReady},
			{"s3 public url unset", ErrS3PublicURLUnset, adminv1.CodeS3PublicURLUnset},
			{"replica not linked", ErrReplicaNotLinked, adminv1.CodeReplicaNotLinked},
			{"replica already linked", ErrReplicaLinked, adminv1.CodeReplicaAlreadyLinked},
			{"rate limited", &RateLimitedError{Scope: "auth_failure", RetryAfter: time.Minute}, adminv1.CodeRateLimited},
			{"validation", &ValidationError{Field: "name", Reason: "empty"}, adminv1.CodeValidationFailed},
			{"bad request", &BadRequestError{Field: "x"}, adminv1.CodeBadRequest},
			{"passthrough api error", &adminv1.APIError{Status: 409, Body: adminv1.ErrorBody{Code: adminv1.CodeNotClaimed}}, adminv1.CodeNotClaimed},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				if got := MapError(test.err).Body.Code; got != test.want {
					t.Fatalf("MapError(%v) = %q, want %q", test.err, got, test.want)
				}
			})
		}
	})

	t.Run("identity sentinels", func(t *testing.T) {
		for _, entry := range identityCodes() {
			if got := MapError(fmt.Errorf("relay: %w", entry.sentinel)).Body.Code; got != entry.code {
				t.Errorf("MapError(%v) = %q, want %q", entry.sentinel, got, entry.code)
			}
		}
		for _, failure := range []identity.FailureCode{
			identity.CodeCredentialsMissing, identity.CodeAlreadyLinked, identity.CodeLoginInProgress, identity.CodeLoginNotActive,
			identity.CodePhoneInvalid, identity.CodeCodeInvalid, identity.CodeCodeExpired, identity.CodePasswordInvalid,
			identity.CodeAttemptsExhausted, identity.CodeAccountMismatch, identity.CodeFloodWait, identity.CodeUnavailable,
			identity.CodeLoginFailed, identity.CodeReplicaSameAccount, identity.CodeCredentialsInvalid,
		} {
			if _, ok := allCodeExpectations()[adminv1.Code(failure)]; !ok {
				t.Errorf("identity failure code %q is not an admin code", failure)
			}
		}
	})
}

func TestMapErrorDetails(t *testing.T) {
	t.Run("flood wait carries retry after", func(t *testing.T) {
		got := MapError(&tgclient.FloodWaitError{Duration: 90 * time.Second})
		if got.Body.RetryAfterSeconds != 90 || !got.Body.Retryable || got.Status != 503 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("outcome unknown carries job id", func(t *testing.T) {
		got := MapError(fmt.Errorf("wrap: %w", &put.OutcomeUnknownError{JobID: "job1", Err: tgclient.ErrSendOutcomeUnknown}))
		if got.Body.Code != adminv1.CodeCommitOutcomeUnknown || got.Body.Details["job_id"] != "job1" || got.Body.RetryAfterSeconds == 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("step error carries attempts left", func(t *testing.T) {
		got := MapError(&identity.StepError{Err: identity.ErrCodeInvalid, AttemptsLeft: 4})
		if got.Body.Code != adminv1.CodeTelegramCodeInvalid || got.Body.Details["attempts_left"] != 4 || !got.Body.Retryable {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("account mismatch carries bound user", func(t *testing.T) {
		got := MapError(&identity.StepError{Err: identity.ErrAccountMismatch, BoundUserID: 777})
		if got.Body.Details["bound_user_id"] != int64(777) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("rate limited carries scope", func(t *testing.T) {
		got := MapError(&RateLimitedError{Scope: "pairing_complete", RetryAfter: 1500 * time.Millisecond})
		if got.Body.Details["scope"] != "pairing_complete" || got.Body.RetryAfterSeconds != 2 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("error text is never exposed", func(t *testing.T) {
		got := MapError(errors.New("secret-token-value in message"))
		if strings.Contains(got.Body.Message, "secret") || strings.Contains(got.Body.Hint, "secret") {
			t.Fatalf("message leaks error text: %+v", got.Body)
		}
	})
}

func TestPanicHandling(t *testing.T) {
	t.Run("ordinary panic becomes an internal envelope", func(t *testing.T) {
		h := newHarness(t)
		h.server.panics = "boom"
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=a"})
		assertError(t, w, 500, adminv1.CodeInternal)
	})
	t.Run("abort handler panic is re-raised", func(t *testing.T) {
		h := newHarness(t)
		h.server.panics = http.ErrAbortHandler
		defer func() {
			if recovered := recover(); recovered != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", recovered)
			}
		}()
		h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=a"})
		t.Fatal("ServeHTTP swallowed http.ErrAbortHandler")
	})
}

func TestClientCancellation(t *testing.T) {
	mapped := MapError(fmt.Errorf("put: %w", context.Canceled))
	if mapped.Status != 499 || mapped.Body.Code != adminv1.CodeInternal || !mapped.Body.Retryable {
		t.Fatalf("MapError(context.Canceled) = %+v", mapped)
	}
	t.Run("response is a bare 499 without a body", func(t *testing.T) {
		h := newHarness(t)
		h.server.err = fmt.Errorf("serve: %w", context.Canceled)
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos/object?key=a"})
		if w.Code != 499 || w.Body.Len() != 0 {
			t.Fatalf("status %d body %q", w.Code, w.Body.String())
		}
	})
	t.Run("no error level log", func(t *testing.T) {
		h := newHarness(t)
		var logs bytes.Buffer
		deps := h.dependencies()
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
		router, err := NewRouter(deps)
		if err != nil {
			t.Fatal(err)
		}
		h.server.err = context.Canceled
		r := httptest.NewRequest(http.MethodGet, "/admin/v1/buckets/photos/object?key=a", nil)
		router.ServeHTTP(httptest.NewRecorder(), r)
		if logs.Len() != 0 {
			t.Fatalf("canceled request logged: %s", logs.String())
		}
	})
}
