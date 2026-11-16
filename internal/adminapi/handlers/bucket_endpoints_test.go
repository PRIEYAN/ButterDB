package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func bucketFixture() bucket.Info {
	return bucket.Info{
		ChannelID:         2045123456,
		Policy:            bucket.PolicyPrivate,
		DefaultEncryption: bucket.EncryptionNone,
		CacheMaxAge:       3600,
		CreatedDate:       testStart.Unix(),
		ObjectCount:       1520,
		StoredBytes:       4231000000,
	}
}

func TestBucketEndpoints(t *testing.T) {
	t.Run("list is sorted by name", func(t *testing.T) {
		h := newHarness(t)
		a, b, c := bucketFixture(), bucketFixture(), bucketFixture()
		a.Name, b.Name, c.Name = "zeta", "alpha", "mid"
		h.buckets.list = []bucket.Info{a, b, c}
		w := h.do(request{method: http.MethodGet, path: adminv1.PathBuckets})
		list := decodeBody[adminv1.BucketList](t, w)
		if len(list.Buckets) != 3 || list.Buckets[0].Name != "alpha" || list.Buckets[1].Name != "mid" || list.Buckets[2].Name != "zeta" {
			t.Fatalf("list = %+v", list)
		}
		if list.Buckets[0].ObjectCount != 1520 || list.Buckets[0].TelegramChannelID != 2045123456 || list.Buckets[0].CacheMaxAgeSeconds != 3600 {
			t.Fatalf("bucket = %+v", list.Buckets[0])
		}
	})
	t.Run("empty list encodes an array", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodGet, path: adminv1.PathBuckets})
		if !strings.Contains(w.Body.String(), `"buckets":[]`) {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
	t.Run("create applies defaults", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.info = bucketFixture()
		w := h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos"}})
		if w.Code != 201 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		spec := h.buckets.created[0]
		if spec.Name != "photos" || spec.Policy != "private" || spec.DefaultEncryption != "none" || spec.CacheMaxAge != 3600 {
			t.Fatalf("spec = %+v", spec)
		}
		if got := decodeBody[adminv1.Bucket](t, w); got.Name != "photos" || got.EncryptionDefault != "none" {
			t.Fatalf("bucket = %+v", got)
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Action != "bucket.create" || h.audit.records[0].Target != "photos" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("create with every field", func(t *testing.T) {
		h := newHarness(t)
		age := 0
		w := h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "site", Policy: "public-read", EncryptionDefault: "tde1", CacheMaxAgeSeconds: &age}})
		if w.Code != 201 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if spec := h.buckets.created[0]; spec.Policy != "public-read" || spec.DefaultEncryption != "tde1" || spec.CacheMaxAge != 0 {
			t.Fatalf("spec = %+v", spec)
		}
	})
	invalid := []struct {
		name   string
		req    adminv1.CreateBucketRequest
		status int
		code   adminv1.Code
		field  string
	}{
		{"uppercase name", adminv1.CreateBucketRequest{Name: "Photos"}, 422, adminv1.CodeBucketNameInvalid, ""},
		{"empty name", adminv1.CreateBucketRequest{}, 422, adminv1.CodeBucketNameInvalid, ""},
		{"bad policy", adminv1.CreateBucketRequest{Name: "photos", Policy: "open"}, 422, adminv1.CodeValidationFailed, "policy"},
		{"bad encryption", adminv1.CreateBucketRequest{Name: "photos", EncryptionDefault: "aes"}, 422, adminv1.CodeValidationFailed, "encryption_default"},
	}
	for _, test := range invalid {
		t.Run("create "+test.name, func(t *testing.T) {
			h := newHarness(t)
			body := assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: test.req}), test.status, test.code)
			if test.field != "" && body.Details["field"] != test.field {
				t.Fatalf("details = %v", body.Details)
			}
			if len(h.buckets.created) != 0 {
				t.Fatal("service reached with an invalid spec")
			}
		})
	}
	t.Run("create cache age bounds", func(t *testing.T) {
		h := newHarness(t)
		age := 31536001
		assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos", CacheMaxAgeSeconds: &age}}), 422, adminv1.CodeValidationFailed)
	})
	serviceErrors := []struct {
		name   string
		err    error
		status int
		code   adminv1.Code
	}{
		{"exists", errs.ErrBucketExists, 409, adminv1.CodeBucketExists},
		{"limit", buckets.ErrBucketLimitReached, 409, adminv1.CodeBucketLimitReached},
		{"flood wait", &tgclient.FloodWaitError{Duration: 7e9}, 503, adminv1.CodeTelegramFloodWait},
		{"not linked", tgclient.ErrNotAuthorized, 409, adminv1.CodeTelegramNotLinked},
		{"telegram unavailable", errs.ErrServiceUnavailable, 503, adminv1.CodeTelegramUnavailable},
	}
	for _, test := range serviceErrors {
		t.Run("create "+test.name, func(t *testing.T) {
			h := newHarness(t)
			h.buckets.err = test.err
			assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos"}}), test.status, test.code)
			if len(h.audit.records) != 1 || h.audit.records[0].Outcome != "failure" || h.audit.records[0].ErrorCode != string(test.code) {
				t.Fatalf("audit = %+v", h.audit.records)
			}
		})
	}
	t.Run("limit details", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.err = buckets.ErrBucketLimitReached
		body := assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos"}}), 409, adminv1.CodeBucketLimitReached)
		if body.Details["limit"] != float64(100) {
			t.Fatalf("details = %v", body.Details)
		}
	})
	t.Run("get", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.info = bucketFixture()
		w := h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos"})
		if got := decodeBody[adminv1.Bucket](t, w); w.Code != 200 || got.Name != "photos" {
			t.Fatalf("status %d bucket %+v", w.Code, got)
		}
	})
	t.Run("get unknown", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.err = errs.ErrNoSuchBucket
		assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/buckets/photos"}), 404, adminv1.CodeBucketNotFound)
	})
	t.Run("patch", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.info = bucketFixture()
		policy, age := "public-read", 60
		w := h.do(request{method: http.MethodPatch, path: "/admin/v1/buckets/photos", body: adminv1.UpdateBucketRequest{Policy: &policy, CacheMaxAgeSeconds: &age}})
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		patch := h.buckets.patched[0]
		if patch.Policy == nil || *patch.Policy != "public-read" || patch.CacheMaxAge == nil || *patch.CacheMaxAge != 60 || patch.DefaultEncryption != nil {
			t.Fatalf("patch = %+v", patch)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "bucket.update" {
			t.Fatalf("audit = %v", got)
		}
	})
	t.Run("patch needs a field", func(t *testing.T) {
		h := newHarness(t)
		assertError(t, h.do(request{method: http.MethodPatch, path: "/admin/v1/buckets/photos", raw: `{}`}), 422, adminv1.CodeValidationFailed)
	})
	t.Run("patch invalid value", func(t *testing.T) {
		h := newHarness(t)
		bad := "weird"
		body := assertError(t, h.do(request{method: http.MethodPatch, path: "/admin/v1/buckets/photos", body: adminv1.UpdateBucketRequest{EncryptionDefault: &bad}}), 422, adminv1.CodeValidationFailed)
		if body.Details["field"] != "encryption_default" {
			t.Fatalf("details = %v", body.Details)
		}
		if len(h.buckets.patched) != 0 {
			t.Fatal("service reached with an invalid patch")
		}
	})
	t.Run("delete", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos"})
		if w.Code != 204 || h.buckets.deleted[0] != "photos" {
			t.Fatalf("status %d deleted %v", w.Code, h.buckets.deleted)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "bucket.delete" {
			t.Fatalf("audit = %v", got)
		}
	})
	t.Run("delete not empty", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.err = errs.ErrBucketNotEmpty
		assertError(t, h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos"}), 409, adminv1.CodeBucketNotEmpty)
	})
}
