package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func keyInfoFixture(id, name string, createdAt int64, revoked bool) access.KeyInfo {
	scope, err := access.NewScope([]string{"photos", "avatars"}, []string{"read", "list", "write"})
	if err != nil {
		panic(err)
	}
	info := access.KeyInfo{ID: access.AccessKeyID(id), Name: name, Scope: scope, CreatedAt: createdAt, LastUsedAt: createdAt + 5}
	if revoked {
		info.RevokedAt = createdAt + 10
	}
	return info
}

func TestAccessKeyEndpoints(t *testing.T) {
	t.Run("list newest first and include_revoked forwarded", func(t *testing.T) {
		h := newHarness(t)
		h.keys.put(keyInfoFixture("BDBK00000000000000A1", "old", 1700000000000, false))
		h.keys.put(keyInfoFixture("BDBK00000000000000B2", "new", 1700000900000, false))
		h.keys.put(keyInfoFixture("BDBK00000000000000C3", "gone", 1700000500000, true))
		w := h.do(request{method: http.MethodGet, path: adminv1.PathAccessKeys})
		list := decodeBody[adminv1.AccessKeyList](t, w)
		if len(list.AccessKeys) != 2 || list.AccessKeys[0].Name != "new" || list.AccessKeys[1].Name != "old" {
			t.Fatalf("list = %+v", list)
		}
		w = h.do(request{method: http.MethodGet, path: adminv1.PathAccessKeys + "?include_revoked=true"})
		list = decodeBody[adminv1.AccessKeyList](t, w)
		if len(list.AccessKeys) != 3 || list.AccessKeys[1].Name != "gone" || list.AccessKeys[1].Status != "revoked" || list.AccessKeys[1].RevokedAt == nil {
			t.Fatalf("list = %+v", list)
		}
		if len(h.keys.include) != 2 || h.keys.include[0] || !h.keys.include[1] {
			t.Fatalf("include flags = %v", h.keys.include)
		}
	})
	t.Run("bad include_revoked", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodGet, path: adminv1.PathAccessKeys + "?include_revoked=maybe"}), 400, adminv1.CodeBadRequest)
	})
	t.Run("create returns the secret once with endpoint settings", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathAccessKeys, body: adminv1.CreateAccessKeyRequest{Name: "website prod", Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read", "list"}}}})
		if w.Code != 201 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		created := decodeBody[adminv1.AccessKeyCreated](t, w)
		if created.SecretAccessKey != h.keys.secret || created.S3Endpoint != "https://s3.example.com" || created.S3Region != "us-east-1" || created.AccessKey.Status != "active" {
			t.Fatalf("created = %+v", created)
		}
		if created.AccessKey.Scope.Actions[0] != "read" || created.AccessKey.Scope.Buckets[0] != "photos" {
			t.Fatalf("scope = %+v", created.AccessKey.Scope)
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Action != "access_key.create" || h.audit.records[0].Target != created.AccessKey.AccessKeyID {
			t.Fatalf("audit = %+v", h.audit.records)
		}
		for _, rec := range h.audit.records {
			if strings.Contains(rec.Target+rec.Action+rec.ErrorCode, h.keys.secret) {
				t.Fatal("audit holds the secret")
			}
		}
	})
	t.Run("secret only appears in the create response", func(t *testing.T) {
		h := newHarness(t)
		h.keys.put(keyInfoFixture("BDBK00000000000000A1", "k", 1700000000000, false))
		for _, target := range []struct{ method, path string }{
			{http.MethodGet, adminv1.PathAccessKeys},
			{http.MethodGet, "/admin/v1/access-keys/BDBK00000000000000A1"},
			{http.MethodDelete, "/admin/v1/access-keys/BDBK00000000000000A1"},
		} {
			if w := h.do(request{method: target.method, path: target.path}); strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("%s %s mentions a secret: %s", target.method, target.path, w.Body.String())
			}
		}
	})
	scopeCases := []struct {
		name  string
		scope adminv1.Scope
	}{
		{"no buckets", adminv1.Scope{Actions: []string{"read"}}},
		{"no actions", adminv1.Scope{Buckets: []string{"photos"}}},
		{"unknown action", adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"admin"}}},
		{"wildcard mixed", adminv1.Scope{Buckets: []string{"*", "photos"}, Actions: []string{"read"}}},
	}
	for _, test := range scopeCases {
		t.Run("create "+test.name, func(t *testing.T) {
			h := newHarness(t)
			assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathAccessKeys, body: adminv1.CreateAccessKeyRequest{Name: "k", Scope: test.scope}}), 422, adminv1.CodeScopeInvalid)
			if len(h.keys.keys) != 0 {
				t.Fatal("key created from an invalid scope")
			}
		})
	}
	t.Run("create name bounds", func(t *testing.T) {
		h := newHarness(t)
		scope := adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read"}}
		for _, name := range []string{"", strings.Repeat("n", 65)} {
			body := assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathAccessKeys, body: adminv1.CreateAccessKeyRequest{Name: name, Scope: scope}}), 422, adminv1.CodeValidationFailed)
			if body.Details["field"] != "name" {
				t.Fatalf("details = %v", body.Details)
			}
		}
	})
	t.Run("get", func(t *testing.T) {
		h := newHarness(t)
		h.keys.put(keyInfoFixture("BDBK00000000000000A1", "k", 1700000000000, false))
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/access-keys/BDBK00000000000000A1"})
		if got := decodeBody[adminv1.AccessKey](t, w); w.Code != 200 || got.AccessKeyID != "BDBK00000000000000A1" || got.LastUsedAt == nil {
			t.Fatalf("status %d key %+v", w.Code, got)
		}
	})
	t.Run("get unknown and malformed ids", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/access-keys/BDBK00000000000000ZZ"}), 404, adminv1.CodeAccessKeyNotFound)
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/access-keys/nonsense"}), 404, adminv1.CodeAccessKeyNotFound)
	})
	t.Run("patch name and scope", func(t *testing.T) {
		h := newHarness(t)
		h.keys.put(keyInfoFixture("BDBK00000000000000A1", "k", 1700000000000, false))
		name := "renamed"
		scope := adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read"}}
		w := h.do(request{method: http.MethodPatch, path: "/admin/v1/access-keys/BDBK00000000000000A1", body: adminv1.UpdateAccessKeyRequest{Name: &name, Scope: &scope}})
		got := decodeBody[adminv1.AccessKey](t, w)
		if w.Code != 200 || got.Name != "renamed" || got.Scope.Buckets[0] != "*" || len(got.Scope.Actions) != 1 {
			t.Fatalf("status %d key %+v", w.Code, got)
		}
		if actions := h.audit.actions(); len(actions) != 1 || actions[0] != "access_key.update" {
			t.Fatalf("audit = %v", actions)
		}
	})
	t.Run("patch needs a field", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodPatch, path: "/admin/v1/access-keys/BDBK00000000000000A1", raw: `{}`}), 422, adminv1.CodeValidationFailed)
		if h.keys.updates != 0 {
			t.Fatal("service reached with an empty patch")
		}
	})
	t.Run("patch invalid scope", func(t *testing.T) {
		h := newHarness(t)
		scope := adminv1.Scope{Buckets: []string{"photos"}}
		assertError(t, h.do(request{method: http.MethodPatch, path: "/admin/v1/access-keys/BDBK00000000000000A1", body: adminv1.UpdateAccessKeyRequest{Scope: &scope}}), 422, adminv1.CodeScopeInvalid)
	})
	t.Run("patch unknown key", func(t *testing.T) {
		h := newHarness(t)
		name := "x"
		assertError(t, h.do(request{method: http.MethodPatch, path: "/admin/v1/access-keys/BDBK00000000000000A1", body: adminv1.UpdateAccessKeyRequest{Name: &name}}), 404, adminv1.CodeAccessKeyNotFound)
	})
	t.Run("revoke returns the revoked key", func(t *testing.T) {
		h := newHarness(t)
		h.keys.put(keyInfoFixture("BDBK00000000000000A1", "k", 1700000000000, false))
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/access-keys/BDBK00000000000000A1"})
		got := decodeBody[adminv1.AccessKey](t, w)
		if w.Code != 200 || got.Status != "revoked" || got.RevokedAt == nil {
			t.Fatalf("status %d key %+v", w.Code, got)
		}
		if actions := h.audit.actions(); len(actions) != 1 || actions[0] != "access_key.revoke" {
			t.Fatalf("audit = %v", actions)
		}
	})
	t.Run("revoke unknown", func(t *testing.T) {
		h := newHarness(t)
		h.keys.err = errs.ErrAccessKeyNotFound
		assertError(t, h.do(request{method: http.MethodDelete, path: "/admin/v1/access-keys/BDBK00000000000000A1"}), 404, adminv1.CodeAccessKeyNotFound)
		if h.audit.records[0].Outcome != "failure" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
}
