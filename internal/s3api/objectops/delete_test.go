package objectops

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func (f *fixture) deleteObject(t *testing.T, key, query string) *deadlineRecorder {
	t.Helper()
	return f.do(t, f.handlers.DeleteObject, router.OpDeleteObject, key, request{
		method: http.MethodDelete, target: "/photos/" + key + query, auth: fullAuth(0),
	})
}

func TestDeleteObject(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		remoteErr  error
		wantStatus int
		wantCode   string
		wantCalls  int
	}{
		{"existing object", "", nil, 204, "", 1},
		{"version id null accepted", "?versionId=null", nil, 204, "", 1},
		{"version id other refused", "?versionId=abc", nil, 400, "InvalidArgument", 0},
		{"flood wait is a slow down", "", errs.ErrSlowDown, 503, "SlowDown", 1},
		{"no such bucket", "", errs.ErrNoSuchBucket, 404, "NoSuchBucket", 1},
		{"unexpected failure", "", errors.New("projection failed"), 500, "InternalError", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			f.remover.errFor = map[string]error{"doc.txt": tc.remoteErr}
			rec := f.deleteObject(t, "doc.txt", tc.query)
			if tc.wantCode == "" {
				if rec.Code != tc.wantStatus || rec.Body.Len() != 0 {
					t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
				}
			} else {
				expectError(t, rec, tc.wantStatus, tc.wantCode)
			}
			if len(f.remover.calls) != tc.wantCalls {
				t.Fatalf("remover calls = %d, want %d", len(f.remover.calls), tc.wantCalls)
			}
		})
	}
}

func TestDeleteObjectAttachesCallerIdentity(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.deleteObject(t, "doc.txt", "")
	if len(f.principals.calls) != 1 || f.principals.calls[0] != (principalCall{accessKeyID: "BDBKEXAMPLE", requestID: testRequestID}) {
		t.Fatalf("principal calls = %+v", f.principals.calls)
	}
	call := f.remover.calls[0]
	if call.ctx.Value(principalContextKey{}) != "BDBKEXAMPLE" {
		t.Fatal("the remover context does not carry the caller identity")
	}
	if call.bucket != testBucket || call.key != "doc.txt" {
		t.Fatalf("call = %+v", call)
	}
}

func TestDeleteObjectPrincipalFailure(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.principals.err = errs.ErrAccessDenied
	expectError(t, f.deleteObject(t, "doc.txt", ""), 403, "AccessDenied")
	if len(f.remover.calls) != 0 {
		t.Fatal("remover was called without a principal")
	}
}

func TestDeleteObjectCommitSurvivesClientDisconnect(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := f.do(t, f.handlers.DeleteObject, router.OpDeleteObject, "doc.txt", request{method: http.MethodDelete, target: "/photos/doc.txt", auth: fullAuth(0), ctx: ctx})
	if rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	if err := f.remover.calls[0].ctx.Err(); err != nil {
		t.Fatalf("commit context was cancelled: %v", err)
	}
}

func TestDeleteObjectAccess(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	cases := []struct {
		name string
		auth func() (bool, []string, []string)
	}{
		{"read and write only key", func() (bool, []string, []string) { return false, []string{"*"}, []string{"read", "write"} }},
		{"anonymous", func() (bool, []string, []string) { return true, nil, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anonymous, buckets, actions := tc.auth()
			auth := anonymousAuth()
			if !anonymous {
				auth = authFor(buckets, actions, 0)
			}
			rec := f.do(t, f.handlers.DeleteObject, router.OpDeleteObject, "doc.txt", request{method: http.MethodDelete, target: "/photos/doc.txt", auth: auth})
			expectError(t, rec, 403, "AccessDenied")
		})
	}
	if len(f.remover.calls) != 0 {
		t.Fatal("remover was called for a denied request")
	}
}
