package s3err

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

func sentinelTable() map[string]struct {
	err    error
	code   Code
	status int
} {
	type row = struct {
		err    error
		code   Code
		status int
	}
	return map[string]row{
		"ErrNoSuchBucket":       {errs.ErrNoSuchBucket, CodeNoSuchBucket, 404},
		"ErrNoSuchKey":          {errs.ErrNoSuchKey, CodeNoSuchKey, 404},
		"ErrBucketExists":       {errs.ErrBucketExists, CodeBucketAlreadyOwnedByYou, 409},
		"ErrBucketNotEmpty":     {errs.ErrBucketNotEmpty, CodeBucketNotEmpty, 409},
		"ErrInvalidBucketName":  {errs.ErrInvalidBucketName, CodeInvalidBucketName, 400},
		"ErrInvalidKey":         {errs.ErrInvalidKey, CodeInvalidArgument, 400},
		"ErrObjectTooLarge":     {errs.ErrObjectTooLarge, CodeEntityTooLarge, 400},
		"ErrMetadataTooLarge":   {errs.ErrMetadataTooLarge, CodeMetadataTooLarge, 400},
		"ErrEmptyFile":          {errs.ErrEmptyFile, CodeInvalidArgument, 400},
		"ErrAccessDenied":       {errs.ErrAccessDenied, CodeAccessDenied, 403},
		"ErrUnknownAccessKey":   {errs.ErrUnknownAccessKey, CodeInvalidAccessKeyId, 403},
		"ErrAccessKeyNotFound":  {errs.ErrAccessKeyNotFound, CodeInvalidAccessKeyId, 403},
		"ErrAccessKeyRevoked":   {errs.ErrAccessKeyRevoked, CodeInvalidAccessKeyId, 403},
		"ErrSignatureMismatch":  {errs.ErrSignatureMismatch, CodeSignatureDoesNotMatch, 403},
		"ErrRequestExpired":     {errs.ErrRequestExpired, CodeAccessDenied, 403},
		"ErrPreconditionFailed": {errs.ErrPreconditionFailed, CodePreconditionFailed, 412},
		"ErrInvalidRange":       {errs.ErrInvalidRange, CodeInvalidRange, 416},
		"ErrBadDigest":          {errs.ErrBadDigest, CodeBadDigest, 400},
		"ErrNoSuchUpload":       {errs.ErrNoSuchUpload, CodeNoSuchUpload, 404},
		"ErrInvalidPart":        {errs.ErrInvalidPart, CodeInvalidPart, 400},
		"ErrInvalidPartOrder":   {errs.ErrInvalidPartOrder, CodeInvalidPartOrder, 400},
		"ErrEntityTooSmall":     {errs.ErrEntityTooSmall, CodeEntityTooSmall, 400},
		"ErrUploadBusy":         {errs.ErrUploadBusy, CodeOperationAborted, 409},
		"ErrSlowDown":           {errs.ErrSlowDown, CodeSlowDown, 503},
		"ErrServiceUnavailable": {errs.ErrServiceUnavailable, CodeServiceUnavailable, 503},
		"ErrNotImplemented":     {errs.ErrNotImplemented, CodeNotImplemented, 501},
		"ErrKeyUnavailable":     {errs.ErrKeyUnavailable, CodeInternalError, 500},
		"ErrMissingDependency":  {errs.ErrMissingDependency, CodeInternalError, 500},
	}
}

func declaredSentinelNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../../domain/errs/errs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errs.go: %v", err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

func TestEveryCodeHasStatus(t *testing.T) {
	seen := map[Code]bool{}
	for _, code := range Codes() {
		t.Run(string(code), func(t *testing.T) {
			if seen[code] {
				t.Fatalf("code %q listed twice", code)
			}
			seen[code] = true
			info, ok := lookup(code)
			if !ok {
				t.Fatalf("code %q has no catalogue entry", code)
			}
			if info.status < 400 || info.status > 599 {
				t.Fatalf("code %q has status %d", code, info.status)
			}
			if strings.TrimSpace(info.message) == "" {
				t.Fatalf("code %q has no default message", code)
			}
			created := New(code, "")
			if created.Code != code || created.Status != info.status || created.Message != info.message {
				t.Fatalf("New(%q) = %+v", code, created)
			}
		})
	}
	if len(seen) != 52 {
		t.Fatalf("catalogue holds %d codes, spec 14.2 lists 52", len(seen))
	}
}

func TestMapEverySentinel(t *testing.T) {
	table := sentinelTable()
	for _, name := range declaredSentinelNames(t) {
		if _, ok := table[name]; !ok {
			t.Errorf("sentinel %s is declared in errs but not covered by this test", name)
		}
	}
	for name, row := range table {
		t.Run(name, func(t *testing.T) {
			for _, err := range []error{row.err, fmt.Errorf("objectrepo: get: %w", row.err), errors.Join(errors.New("other"), row.err)} {
				got := Map(err)
				if got.Code != row.code || got.Status != row.status {
					t.Fatalf("Map(%v) = %s/%d, want %s/%d", err, got.Code, got.Status, row.code, row.status)
				}
				if got.Message == "" {
					t.Fatalf("Map(%v) has no message", err)
				}
			}
		})
	}
}

func TestMapTypedAndLocalErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		code       Code
		status     int
		retryAfter time.Duration
		extra      map[string]string
	}{
		{"nil", nil, CodeInternalError, 500, 0, nil},
		{"unmapped", errors.New("boom"), CodeInternalError, 500, 0, nil},
		{"slow down carries retry after", &admission.SlowDownError{RetryAfter: 17 * time.Second, Reason: "limiter"}, CodeSlowDown, 503, 17 * time.Second, nil},
		{"slow down wrapped", fmt.Errorf("put: %w", &admission.SlowDownError{RetryAfter: 2 * time.Second}), CodeSlowDown, 503, 2 * time.Second, nil},
		{"bare slow down gets default retry", errs.ErrSlowDown, CodeSlowDown, 503, 5 * time.Second, nil},
		{"spool budget exhausted", spool.ErrBudgetExhausted, CodeSlowDown, 503, 5 * time.Second, nil},
		{"unsatisfiable range", &httprange.UnsatisfiableRangeError{Requested: "bytes=9-", Size: 4}, CodeInvalidRange, 416, 0, map[string]string{"RangeRequested": "bytes=9-", "ActualObjectSize": "4"}},
		{"precondition", &httprange.PreconditionError{Condition: "If-Match"}, CodePreconditionFailed, 412, 0, map[string]string{"Condition": "If-Match"}},
		{"commit outcome unknown", &put.OutcomeUnknownError{JobID: "j1", Err: errors.New("send")}, CodeSlowDown, 503, 30 * time.Second, nil},
		{"commit outcome unknown sentinel", put.ErrCommitOutcomeUnknown, CodeSlowDown, 503, 30 * time.Second, nil},
		{"idempotency conflict", put.ErrIdempotencyConflict, CodeOperationAborted, 409, 0, nil},
		{"put invalid request", fmt.Errorf("put: %w: x", put.ErrInvalidRequest), CodeInvalidArgument, 400, 0, nil},
		{"put invalid key", fmt.Errorf("put: %w: %w", errs.ErrInvalidKey, errors.New("x")), CodeInvalidArgument, 400, 0, nil},
		{"key too long", fmt.Errorf("object: key: %w: %w", object.ErrInvalidKey, object.ErrKeyTooLong), CodeKeyTooLongError, 400, 0, nil},
		{"key control character", fmt.Errorf("object: key: %w: %w", object.ErrInvalidKey, object.ErrKeyControlCharacter), CodeInvalidArgument, 400, 0, nil},
		{"metadata too large", object.ErrMetadataTooLarge, CodeMetadataTooLarge, 400, 0, nil},
		{"domain bucket name", fmt.Errorf("bucket: name: %w: x", bucket.ErrInvalidBucketName), CodeInvalidBucketName, 400, 0, nil},
		{"spool length mismatch", spool.ErrLengthMismatch, CodeIncompleteBody, 400, 0, nil},
		{"put job aborted is internal", put.ErrJobAborted, CodeInternalError, 500, 0, nil},
		{"control projection failure is internal", put.ErrControlProjection, CodeInternalError, 500, 0, nil},
		{"xml too large", xmlcodec.ErrBodyTooLarge, CodeMaxMessageLengthExceeded, 400, 0, nil},
		{"xml empty", fmt.Errorf("decode: %w", xmlcodec.ErrEmptyBody), CodeMissingRequestBodyError, 400, 0, nil},
		{"xml malformed", xmlcodec.ErrMalformed, CodeMalformedXML, 400, 0, nil},
		{"xml doctype", xmlcodec.ErrDoctype, CodeMalformedXML, 400, 0, nil},
		{"entity too large has max size", errs.ErrObjectTooLarge, CodeEntityTooLarge, 400, 0, map[string]string{"MaxSizeAllowed": "1992294400"}},
		{"entity too small has min size", errs.ErrEntityTooSmall, CodeEntityTooSmall, 400, 0, map[string]string{"MinSizeAllowed": "5242880"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Map(tc.err)
			if got.Code != tc.code || got.Status != tc.status {
				t.Fatalf("Map = %s/%d, want %s/%d", got.Code, got.Status, tc.code, tc.status)
			}
			if got.RetryAfter != tc.retryAfter {
				t.Fatalf("RetryAfter = %s, want %s", got.RetryAfter, tc.retryAfter)
			}
			for name, want := range tc.extra {
				if value, ok := extraValue(got, name); !ok || value != want {
					t.Fatalf("Extra %s = %q (present %v), want %q", name, value, ok, want)
				}
			}
		})
	}
}

func TestMapKeepsExplicitAPIError(t *testing.T) {
	want := New(CodeMethodNotAllowed, "nope")
	want.Extra = []Field{{Name: "Method", Value: "PATCH"}}
	got := Map(fmt.Errorf("router: %w", want))
	if got.Code != CodeMethodNotAllowed || got.Status != http.StatusMethodNotAllowed || got.Message != "nope" {
		t.Fatalf("Map = %+v", got)
	}
	if value, _ := extraValue(got, "Method"); value != "PATCH" {
		t.Fatalf("Extra lost: %+v", got.Extra)
	}
}

func extraValue(e APIError, name string) (string, bool) {
	for _, field := range e.Extra {
		if field.Name == name {
			return field.Value, true
		}
	}
	return "", false
}

func TestMapSigV4Reasons(t *testing.T) {
	served := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		in     sigv4.Error
		code   Code
		status int
	}{
		{"signature", sigv4.Error{Reason: sigv4.ReasonSignature, AccessKeyID: "BDBKX"}, CodeSignatureDoesNotMatch, 403},
		{"unknown key", sigv4.Error{Reason: sigv4.ReasonUnknownKey, AccessKeyID: "BDBKX"}, CodeInvalidAccessKeyId, 403},
		{"header malformed", sigv4.Error{Reason: sigv4.ReasonHeaderMalformed}, CodeAuthorizationHeaderMalformed, 400},
		{"query malformed", sigv4.Error{Reason: sigv4.ReasonQueryMalformed}, CodeAuthorizationQueryParametersError, 400},
		{"wrong region header", sigv4.Error{Reason: sigv4.ReasonWrongRegion, Region: "eu-west-1"}, CodeAuthorizationHeaderMalformed, 400},
		{"skew", sigv4.Error{Reason: sigv4.ReasonSkew, RequestTime: served, ServerTime: served}, CodeRequestTimeTooSkewed, 403},
		{"expired", sigv4.Error{Reason: sigv4.ReasonExpired, Expires: served, ServerTime: served}, CodeAccessDenied, 403},
		{"not yet valid", sigv4.Error{Reason: sigv4.ReasonNotYetValid}, CodeAccessDenied, 403},
		{"missing date", sigv4.Error{Reason: sigv4.ReasonMissingDate}, CodeAccessDenied, 403},
		{"missing content sha", sigv4.Error{Reason: sigv4.ReasonMissingContentSHA256}, CodeInvalidRequest, 400},
		{"bad content sha", sigv4.Error{Reason: sigv4.ReasonBadContentSHA256}, CodeInvalidArgument, 400},
		{"content sha mismatch", sigv4.Error{Reason: sigv4.ReasonContentSHA256Mismatch, ClientSHA256: "aa", ServerSHA256: "bb"}, CodeXAmzContentSHA256Mismatch, 400},
		{"security token", sigv4.Error{Reason: sigv4.ReasonSecurityToken}, CodeInvalidToken, 400},
		{"sigv2", sigv4.Error{Reason: sigv4.ReasonSigV2}, CodeInvalidRequest, 400},
		{"sigv4a", sigv4.Error{Reason: sigv4.ReasonSigV4A}, CodeNotImplemented, 501},
		{"both mechanisms", sigv4.Error{Reason: sigv4.ReasonBothMechanisms}, CodeInvalidArgument, 400},
		{"unsupported scheme", sigv4.Error{Reason: sigv4.ReasonUnsupportedScheme}, CodeInvalidArgument, 400},
		{"chunk framing", sigv4.Error{Reason: sigv4.ReasonChunkFraming}, CodeInvalidRequest, 400},
		{"incomplete body", sigv4.Error{Reason: sigv4.ReasonIncompleteBody}, CodeIncompleteBody, 400},
		{"checksum mismatch", sigv4.Error{Reason: sigv4.ReasonChecksumMismatch, Algorithm: "CRC32"}, CodeBadDigest, 400},
		{"checksum malformed", sigv4.Error{Reason: sigv4.ReasonChecksumMalformed}, CodeInvalidRequest, 400},
		{"unknown reason falls back to sentinel", sigv4.Error{Reason: 99, Err: errs.ErrAccessDenied}, CodeAccessDenied, 403},
		{"unknown reason without sentinel", sigv4.Error{Reason: 99}, CodeInternalError, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapSigV4(&tc.in)
			if got.Code != tc.code || got.Status != tc.status {
				t.Fatalf("MapSigV4 = %s/%d, want %s/%d", got.Code, got.Status, tc.code, tc.status)
			}
		})
	}
}

func TestMapFindsSigV4ErrorInChain(t *testing.T) {
	failure, ok := any(&sigv4.Error{Reason: sigv4.ReasonUnknownKey, AccessKeyID: "BDBKX"}).(error)
	if !ok {
		t.Skip("sigv4.Error implements error only once the sigv4 lane is merged")
	}
	got := Map(fmt.Errorf("auth: %w", failure))
	if got.Code != CodeInvalidAccessKeyId {
		t.Fatalf("Map = %s", got.Code)
	}
}
