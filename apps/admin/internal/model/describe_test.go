package model

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func apiError(status int, code adminv1.Code, mutate func(*adminv1.ErrorBody)) *adminv1.APIError {
	body := adminv1.ErrorBody{Code: code, Message: "server words " + string(code), Hint: "server hint", RequestID: "req-1"}
	if mutate != nil {
		mutate(&body)
	}
	return &adminv1.APIError{Status: status, Body: body}
}

func declaredCodes(t *testing.T) []adminv1.Code {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../../../api/adminv1/errors.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errors.go: %v", err)
	}
	var codes []adminv1.Code
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Code" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", lit.Value, err)
			}
			codes = append(codes, adminv1.Code(value))
		}
	}
	return codes
}

func TestDescribeCoversEveryCode(t *testing.T) {
	t.Parallel()
	codes := declaredCodes(t)
	if len(codes) < 60 {
		t.Fatalf("found %d declared codes, want at least 60", len(codes))
	}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			if _, ok := lookup(code); !ok {
				t.Fatalf("code %q has no catalogue entry", code)
			}
			p := Describe(apiError(400, code, nil), testNow)
			if p.Code != code {
				t.Fatalf("Code = %q, want %q", p.Code, code)
			}
			if p.Title == "" || p.Presentation == "" {
				t.Fatalf("Title = %q, Presentation = %q, want both set", p.Title, p.Presentation)
			}
			if strings.Contains(p.Title, "server words") {
				t.Fatalf("Title %q leaks server message text for a known code", p.Title)
			}
			if p.Hint != "server hint" || p.RequestID != "req-1" {
				t.Fatalf("Hint = %q, RequestID = %q", p.Hint, p.RequestID)
			}
			if p.Presentation == PresentRoute && p.Route == "" {
				t.Fatalf("route presentation without a route")
			}
			if p.Details[detailHTTPStatus] != 400 {
				t.Fatalf("Details[%s] = %v, want 400", detailHTTPStatus, p.Details[detailHTTPStatus])
			}
		})
	}
}

func TestDescribeMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		err           error
		wantPresent   Presentation
		wantRoute     Route
		wantField     string
		wantTitle     string
		wantRetryable bool
	}{
		{name: "bad request", err: apiError(400, adminv1.CodeBadRequest, nil), wantPresent: PresentDialog, wantTitle: textVersionMismatch},
		{name: "length required", err: apiError(411, adminv1.CodeLengthRequired, nil), wantPresent: PresentDialog, wantTitle: textVersionMismatch},
		{name: "validation with field", err: apiError(422, adminv1.CodeValidationFailed, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"field": "label", "reason": "too long"}
		}), wantPresent: PresentInline, wantField: "label", wantTitle: "Check this value: too long"},
		{name: "validation without field", err: apiError(422, adminv1.CodeValidationFailed, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"reason": "too long"}
		}), wantPresent: PresentDialog, wantTitle: "Check this value: too long"},
		{name: "unauthenticated", err: apiError(401, adminv1.CodeUnauthenticated, nil), wantPresent: PresentRoute, wantRoute: RouteConnect, wantTitle: textNotSignedIn},
		{name: "device revoked", err: apiError(401, adminv1.CodeDeviceRevoked, nil), wantPresent: PresentRoute, wantRoute: RouteConnect, wantTitle: textRevoked},
		{name: "rate limited", err: apiError(429, adminv1.CodeRateLimited, func(b *adminv1.ErrorBody) {
			b.RetryAfterSeconds = 12
		}), wantPresent: PresentBanner, wantTitle: "Too many attempts. Try again in 12 s.", wantRetryable: true},
		{name: "internal", err: apiError(500, adminv1.CodeInternal, nil), wantPresent: PresentDialog, wantTitle: "The server hit an error. Check its log for request req-1."},
		{name: "server not ready", err: apiError(503, adminv1.CodeServerNotReady, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"state": "rebuilding"}
		}), wantPresent: PresentBanner, wantTitle: "The server is rebuilding. Waiting…", wantRetryable: true},
		{name: "not implemented", err: apiError(501, adminv1.CodeNotImplemented, nil), wantPresent: PresentInline, wantTitle: textNotImplemented},
		{name: "already claimed", err: apiError(409, adminv1.CodeAlreadyClaimed, nil), wantPresent: PresentRoute, wantRoute: RouteConnect, wantTitle: textAlreadyClaimed},
		{name: "setup token invalid", err: apiError(401, adminv1.CodeSetupTokenInvalid, nil), wantPresent: PresentInline, wantField: fieldSetupToken, wantTitle: textSetupTokenInvalid},
		{name: "last device", err: apiError(409, adminv1.CodeLastDevice, nil), wantPresent: PresentDialog, wantTitle: textLastDevice},
		{name: "telegram not linked", err: apiError(409, adminv1.CodeTelegramNotLinked, nil), wantPresent: PresentRoute, wantRoute: RouteTelegram, wantTitle: textTelegramNotLinked},
		{name: "telegram unavailable", err: apiError(503, adminv1.CodeTelegramUnavailable, nil), wantPresent: PresentBanner, wantTitle: textTelegramUnavailable, wantRetryable: true},
		{name: "code invalid", err: apiError(422, adminv1.CodeTelegramCodeInvalid, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"attempts_left": float64(4)}
		}), wantPresent: PresentInline, wantField: fieldCode, wantTitle: "That code is not right. 4 attempts left."},
		{name: "password invalid", err: apiError(422, adminv1.CodeTelegramPasswordInvalid, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"attempts_left": 2}
		}), wantPresent: PresentInline, wantField: fieldPassword, wantTitle: "Wrong password. 2 attempts left."},
		{name: "code expired", err: apiError(410, adminv1.CodeTelegramCodeExpired, nil), wantPresent: PresentInline, wantField: fieldPhone, wantTitle: textCodeExpired},
		{name: "phone banned", err: apiError(403, adminv1.CodeTelegramPhoneBanned, nil), wantPresent: PresentDialog, wantTitle: textPhoneBanned},
		{name: "account mismatch", err: apiError(409, adminv1.CodeTelegramAccountMismatch, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"bound_user_id": "12345"}
		}), wantPresent: PresentDialog, wantTitle: "This server is bound to Telegram user 12345. Log in with that account."},
		{name: "bucket exists", err: apiError(409, adminv1.CodeBucketExists, nil), wantPresent: PresentInline, wantField: fieldName, wantTitle: textBucketExists},
		{name: "bucket name invalid", err: apiError(422, adminv1.CodeBucketNameInvalid, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"reason": "uppercase letters are not allowed"}
		}), wantPresent: PresentInline, wantField: fieldName, wantTitle: "Check this value: uppercase letters are not allowed"},
		{name: "bucket not empty", err: apiError(409, adminv1.CodeBucketNotEmpty, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"object_count": float64(7)}
		}), wantPresent: PresentDialog, wantTitle: "7 objects remain. Delete them first."},
		{name: "bucket limit", err: apiError(409, adminv1.CodeBucketLimitReached, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"limit": float64(100)}
		}), wantPresent: PresentDialog, wantTitle: "This server allows at most 100 buckets."},
		{name: "scope invalid", err: apiError(422, adminv1.CodeScopeInvalid, nil), wantPresent: PresentInline, wantField: fieldScope, wantTitle: textScopeInvalid},
		{name: "scope invalid server field", err: apiError(422, adminv1.CodeScopeInvalid, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"field": "actions"}
		}), wantPresent: PresentInline, wantField: "actions", wantTitle: textScopeInvalid},
		{name: "object too large", err: apiError(413, adminv1.CodeObjectTooLarge, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"max_bytes": float64(1900 * 1024 * 1024)}
		}), wantPresent: PresentInline, wantTitle: "Too large (max 1.9 GiB)."},
		{name: "metadata too large", err: apiError(422, adminv1.CodeMetadataTooLarge, func(b *adminv1.ErrorBody) {
			b.Details = map[string]any{"max_bytes": float64(2048)}
		}), wantPresent: PresentInline, wantField: fieldMetadata, wantTitle: "Metadata is limited to 2048 bytes."},
		{name: "commit outcome unknown", err: apiError(503, adminv1.CodeCommitOutcomeUnknown, func(b *adminv1.ErrorBody) {
			b.RetryAfterSeconds = 5
		}), wantPresent: PresentInline, wantTitle: textCommitUnknown, wantRetryable: true},
		{name: "presign key invalid", err: apiError(422, adminv1.CodePresignKeyInvalid, nil), wantPresent: PresentInline, wantField: fieldSigningKey, wantTitle: textPresignKeyInvalid},
		{name: "public url unset", err: apiError(409, adminv1.CodeS3PublicURLUnset, nil), wantPresent: PresentDialog, wantTitle: textPublicURLUnset},
		{name: "replica not linked", err: apiError(409, adminv1.CodeReplicaNotLinked, nil), wantPresent: PresentBanner, wantTitle: textReplicaChanged},
		{name: "wrapped api error", err: fmt.Errorf("buckets: %w", apiError(404, adminv1.CodeBucketNotFound, nil)), wantPresent: PresentBanner, wantTitle: textBucketNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := Describe(tc.err, testNow)
			if p.Presentation != tc.wantPresent {
				t.Errorf("Presentation = %q, want %q", p.Presentation, tc.wantPresent)
			}
			if p.Route != tc.wantRoute {
				t.Errorf("Route = %q, want %q", p.Route, tc.wantRoute)
			}
			if p.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", p.Field, tc.wantField)
			}
			if p.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", p.Title, tc.wantTitle)
			}
			if p.Retryable != tc.wantRetryable {
				t.Errorf("Retryable = %v, want %v", p.Retryable, tc.wantRetryable)
			}
		})
	}
}

func TestDescribeRetryAfterSetsRetryAt(t *testing.T) {
	t.Parallel()
	p := Describe(apiError(429, adminv1.CodeRateLimited, func(b *adminv1.ErrorBody) { b.RetryAfterSeconds = 30 }), testNow)
	if want := testNow.Add(30 * time.Second); !p.RetryAt.Equal(want) {
		t.Fatalf("RetryAt = %v, want %v", p.RetryAt, want)
	}
	none := Describe(apiError(404, adminv1.CodeBucketNotFound, nil), testNow)
	if !none.RetryAt.IsZero() {
		t.Fatalf("RetryAt = %v, want zero", none.RetryAt)
	}
}

func TestDescribeRevokedBodyCarriesTime(t *testing.T) {
	t.Parallel()
	p := Describe(apiError(401, adminv1.CodeDeviceRevoked, func(b *adminv1.ErrorBody) {
		b.Details = map[string]any{"revoked_at": "2026-11-15T08:00:00Z"}
	}), testNow)
	if want := "This device was revoked at 2026-11-15T08:00:00Z."; p.Body != want {
		t.Fatalf("Body = %q, want %q", p.Body, want)
	}
}

func TestDescribeFloodWaitUsesUntil(t *testing.T) {
	t.Parallel()
	until := testNow.Add(90 * time.Second)
	cases := []struct {
		name      string
		mutate    func(*adminv1.ErrorBody)
		wantAt    time.Time
		wantTitle string
	}{
		{
			name: "until wins over retry after",
			mutate: func(b *adminv1.ErrorBody) {
				b.RetryAfterSeconds = 10
				b.Details = map[string]any{"until": until.Format(time.RFC3339)}
			},
			wantAt:    until,
			wantTitle: "Telegram asked the server to wait until 09:31:30.",
		},
		{
			name:      "retry after used when until missing",
			mutate:    func(b *adminv1.ErrorBody) { b.RetryAfterSeconds = 10 },
			wantAt:    testNow.Add(10 * time.Second),
			wantTitle: "Telegram asked the server to wait until 09:30:10.",
		},
		{
			name: "unparsable until falls back to retry after",
			mutate: func(b *adminv1.ErrorBody) {
				b.RetryAfterSeconds = 20
				b.Details = map[string]any{"until": "soon"}
			},
			wantAt:    testNow.Add(20 * time.Second),
			wantTitle: "Telegram asked the server to wait until 09:30:20.",
		},
		{
			name:      "no time at all",
			mutate:    nil,
			wantAt:    time.Time{},
			wantTitle: "Telegram asked the server to wait before the next request.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := Describe(apiError(503, adminv1.CodeTelegramFloodWait, tc.mutate), testNow)
			if !p.RetryAt.Equal(tc.wantAt) {
				t.Errorf("RetryAt = %v, want %v", p.RetryAt, tc.wantAt)
			}
			if p.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", p.Title, tc.wantTitle)
			}
			if p.Presentation != PresentBanner || !p.Retryable {
				t.Errorf("Presentation = %q, Retryable = %v", p.Presentation, p.Retryable)
			}
		})
	}
}

func TestDescribePinMismatch(t *testing.T) {
	t.Parallel()
	mismatch := &adminv1.PinMismatchError{Expected: "SHA256:aa", Got: "SHA256:bb"}
	for _, err := range []error{mismatch, fmt.Errorf("connect: %w", mismatch)} {
		p := Describe(err, testNow)
		if p.Presentation != PresentRoute || p.Route != RouteConnect {
			t.Fatalf("Presentation = %q, Route = %q", p.Presentation, p.Route)
		}
		if p.Title != "Server identity changed" {
			t.Fatalf("Title = %q", p.Title)
		}
		if p.Details["expected"] != "SHA256:aa" || p.Details["got"] != "SHA256:bb" {
			t.Fatalf("Details = %v", p.Details)
		}
		if p.Retryable {
			t.Fatalf("pin mismatch must not be retryable")
		}
	}
}

func TestDescribeUnknownCodeFallsBack(t *testing.T) {
	t.Parallel()
	p := Describe(apiError(418, adminv1.Code("future_code"), func(b *adminv1.ErrorBody) {
		b.Message = "The teapot refuses."
		b.Hint = "Brew something else."
	}), testNow)
	if p.Presentation != PresentDialog {
		t.Fatalf("Presentation = %q, want dialog", p.Presentation)
	}
	if p.Title != "The teapot refuses." || p.Hint != "Brew something else." {
		t.Fatalf("Title = %q, Hint = %q", p.Title, p.Hint)
	}
	if !strings.Contains(p.Body, "future_code") || p.Code != "future_code" {
		t.Fatalf("Body = %q, Code = %q", p.Body, p.Code)
	}
	empty := Describe(apiError(418, adminv1.Code("future_code"), func(b *adminv1.ErrorBody) { b.Message = "" }), testNow)
	if empty.Title != textUnknownCode {
		t.Fatalf("Title = %q, want %q", empty.Title, textUnknownCode)
	}
}

func TestDescribeNonAPIErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		err         error
		wantPresent Presentation
		wantTitle   string
		wantRetry   bool
	}{
		{name: "nil", err: nil, wantPresent: "", wantTitle: ""},
		{name: "context canceled", err: context.Canceled, wantPresent: "", wantTitle: ""},
		{name: "wrapped context canceled", err: fmt.Errorf("adminv1: GET /x: %w", context.Canceled), wantPresent: "", wantTitle: ""},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantPresent: PresentBanner, wantTitle: textUnreachable, wantRetry: true},
		{name: "dial error", err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, wantPresent: PresentBanner, wantTitle: textUnreachable, wantRetry: true},
		{name: "connection reset eof", err: fmt.Errorf("adminv1: PUT /x: %w", io.ErrUnexpectedEOF), wantPresent: PresentBanner, wantTitle: textUnreachable, wantRetry: true},
		{name: "unknown authority", err: fmt.Errorf("adminv1: GET /x: %w", x509.UnknownAuthorityError{}), wantPresent: PresentDialog, wantTitle: textCertificateUntrust},
		{name: "certificate verification", err: &tls.CertificateVerificationError{Err: x509.HostnameError{Host: "h"}}, wantPresent: PresentDialog, wantTitle: textCertificateUntrust},
		{name: "unexpected", err: errors.New("disk exploded"), wantPresent: PresentDialog, wantTitle: textUnexpected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := Describe(tc.err, testNow)
			if p.Presentation != tc.wantPresent || p.Title != tc.wantTitle || p.Retryable != tc.wantRetry {
				t.Fatalf("got Presentation %q, Title %q, Retryable %v; want %q, %q, %v",
					p.Presentation, p.Title, p.Retryable, tc.wantPresent, tc.wantTitle, tc.wantRetry)
			}
		})
	}
}

func TestFormatBytesMatchesShellContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int64
		want string
	}{
		{-5, "0 B"}, {0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"}, {1900 * 1024 * 1024, "1.9 GiB"}, {1 << 40, "1.0 TiB"}, {1<<63 - 1, "8.0 EiB"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.n); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
