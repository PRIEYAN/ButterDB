package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
)

func TestStatusAndUsageEndpoints(t *testing.T) {
	t.Run("status passes through and normalises components", func(t *testing.T) {
		h := newHarness(t)
		h.status.status = adminv1.Status{ServerID: "srv", State: "ready", ServerVersion: "0.1.0"}
		w := h.do(request{method: http.MethodGet, path: adminv1.PathStatus})
		if w.Code != 200 {
			t.Fatalf("status = %d", w.Code)
		}
		if got := decodeBody[adminv1.Status](t, w); got.ServerID != "srv" || got.State != "ready" {
			t.Fatalf("status = %+v", got)
		}
		if !strings.Contains(w.Body.String(), `"components":[]`) {
			t.Fatalf("components must be an empty array: %s", w.Body.String())
		}
	})
	t.Run("usage passes through", func(t *testing.T) {
		h := newHarness(t)
		h.status.usage = adminv1.Usage{Buckets: 3, Objects: 18234, StoredBytes: 52934893211}
		w := h.do(request{method: http.MethodGet, path: adminv1.PathUsage})
		if got := decodeBody[adminv1.Usage](t, w); w.Code != 200 || got.Buckets != 3 || got.Objects != 18234 {
			t.Fatalf("status %d usage %+v", w.Code, got)
		}
	})
	t.Run("source failure maps", func(t *testing.T) {
		h := newHarness(t)
		h.status.err = ErrServerNotReady
		body := assertError(t, h.do(request{method: http.MethodGet, path: adminv1.PathStatus}), 503, adminv1.CodeServerNotReady)
		if !body.Retryable {
			t.Fatalf("body = %+v", body)
		}
		assertError(t, h.do(request{method: http.MethodGet, path: adminv1.PathUsage}), 503, adminv1.CodeServerNotReady)
	})
}

func TestAuditEndpoint(t *testing.T) {
	t.Run("defaults and page", func(t *testing.T) {
		h := newHarness(t)
		h.audit.page = adminv1.AuditPage{
			Events:     []adminv1.AuditEvent{{ID: 5521, Action: "bucket.create", Target: "photos", Outcome: "success"}},
			NextCursor: "next",
		}
		w := h.do(request{method: http.MethodGet, path: adminv1.PathAudit})
		if w.Code != 200 {
			t.Fatalf("status = %d", w.Code)
		}
		page := decodeBody[adminv1.AuditPage](t, w)
		if len(page.Events) != 1 || page.Events[0].ID != 5521 || page.NextCursor != "next" {
			t.Fatalf("page = %+v", page)
		}
		q := h.audit.query
		if q.Limit != 100 || q.Cursor != "" || q.Action != "" || !q.Since.IsZero() || !q.Until.IsZero() {
			t.Fatalf("query = %+v", q)
		}
	})
	t.Run("empty page encodes an array", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodGet, path: adminv1.PathAudit})
		if !strings.Contains(w.Body.String(), `"events":[]`) {
			t.Fatalf("body = %s", w.Body.String())
		}
	})
	t.Run("filters are forwarded", func(t *testing.T) {
		h := newHarness(t)
		h.do(request{method: http.MethodGet, path: "/admin/v1/audit?cursor=c1&limit=500&action=bucket.&since=2026-11-01T00:00:00Z&until=2026-11-12T09:41:07%2B02:00"})
		q := h.audit.query
		wantSince := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		wantUntil := time.Date(2026, 11, 12, 7, 41, 7, 0, time.UTC)
		if q.Cursor != "c1" || q.Limit != 500 || q.Action != "bucket." || !q.Since.Equal(wantSince) || !q.Until.Equal(wantUntil) {
			t.Fatalf("query = %+v", q)
		}
	})
	for _, query := range []string{"limit=0", "limit=501", "limit=x", "since=yesterday", "until=2026-11-12"} {
		t.Run("bad "+query, func(t *testing.T) {
			h := newHarness(t)
			assertError(t, h.do(request{method: http.MethodGet, path: "/admin/v1/audit?" + query}), 400, adminv1.CodeBadRequest)
		})
	}
	t.Run("store failure", func(t *testing.T) {
		h := newHarness(t)
		h.audit.err = errors.New("db down")
		assertError(t, h.do(request{method: http.MethodGet, path: adminv1.PathAudit}), 500, adminv1.CodeInternal)
	})
}

func TestAuditRecordsMutations(t *testing.T) {
	h := newHarness(t)
	h.buckets.info = bucketFixture()
	h.keys.put(keyInfoFixture("BDBK00000000000000A1", "k", 1700000000000, false))
	h.keys.put(presignKeyFixture([]string{"photos"}, []string{"read"}, false))
	h.lister.meta = objectMetaFixture()
	h.writer.meta = objectMetaFixture()
	h.setup.relay.snapshot = identity.Snapshot{State: identity.StateReady}
	h.setup.result = adminv1.ClaimResult{Device: adminv1.Device{DeviceID: "new", Name: "n"}}
	h.pairing.result = adminv1.DeviceTokenResult{Device: adminv1.Device{DeviceID: "paired", Name: "p"}}
	h.setup.info = adminv1.ServerInfo{ServerID: "srv"}
	policy, name := "public-read", "renamed"
	scope := adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read"}}
	mutations := []struct {
		action string
		req    request
	}{
		{"setup.claim", request{method: http.MethodPost, path: adminv1.PathSetupClaim, body: claimBody()}},
		{"pairing.request", request{method: http.MethodPost, path: adminv1.PathPairingRequest, body: adminv1.PairingRequest{Delivery: "saved_messages", DeviceName: "d"}}},
		{"pairing.complete", request{method: http.MethodPost, path: adminv1.PathPairingComplete, body: adminv1.PairingCompleteRequest{Code: "c", DeviceName: "d"}}},
		{"device.rename", request{method: http.MethodPatch, path: "/admin/v1/devices/devX", body: adminv1.UpdateDeviceRequest{Name: "x"}}},
		{"device.rotate", request{method: http.MethodPost, path: adminv1.PathDeviceSelfRotate}},
		{"device.revoke", request{method: http.MethodDelete, path: "/admin/v1/devices/devX"}},
		{"telegram.credentials_save", request{method: http.MethodPost, path: adminv1.PathSetupTelegramApp, body: adminv1.TelegramCredentialsRequest{APIID: 1, APIHash: strings.Repeat("a", 32)}}},
		{"telegram.credentials_delete", request{method: http.MethodDelete, path: adminv1.PathSetupTelegramApp}},
		{"telegram.login_start", request{method: http.MethodPost, path: adminv1.PathSetupTelegramPhone, body: adminv1.TelegramLoginStartRequest{Phone: "+447700900134"}}},
		{"telegram.login_cancel", request{method: http.MethodPost, path: adminv1.PathSetupTelegramCancel}},
		{"telegram.unlink", request{method: http.MethodPost, path: adminv1.PathSetupTelegramUnlink, body: adminv1.UnlinkRequest{Confirm: "unlink"}}},
		{"bucket.create", request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos"}}},
		{"bucket.update", request{method: http.MethodPatch, path: "/admin/v1/buckets/photos", body: adminv1.UpdateBucketRequest{Policy: &policy}}},
		{"bucket.delete", request{method: http.MethodDelete, path: "/admin/v1/buckets/photos"}},
		{"access_key.create", request{method: http.MethodPost, path: adminv1.PathAccessKeys, body: adminv1.CreateAccessKeyRequest{Name: "k2", Scope: scope}}},
		{"access_key.update", request{method: http.MethodPatch, path: "/admin/v1/access-keys/BDBK00000000000000A1", body: adminv1.UpdateAccessKeyRequest{Name: &name}}},
		{"access_key.revoke", request{method: http.MethodDelete, path: "/admin/v1/access-keys/BDBK00000000000000A1"}},
		{"object.upload", uploadRequest("/admin/v1/buckets/photos/object?key=a", "data", nil)},
		{"object.delete", request{method: http.MethodDelete, path: "/admin/v1/buckets/photos/object?key=a"}},
		{"object.delete_batch", request{method: http.MethodPost, path: "/admin/v1/buckets/photos/objects/delete", body: adminv1.DeleteObjectsRequest{Keys: []string{"a"}}}},
		{"object.presign", request{method: http.MethodPost, path: "/admin/v1/buckets/photos/presign", body: adminv1.PresignRequest{Key: "a", AccessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y"}}},
	}
	for _, mutation := range mutations {
		before := len(h.audit.records)
		w := h.do(mutation.req)
		if w.Code >= 300 {
			t.Fatalf("%s: status = %d: %s", mutation.action, w.Code, w.Body.String())
		}
		if len(h.audit.records) != before+1 {
			t.Fatalf("%s wrote %d audit records, want 1", mutation.action, len(h.audit.records)-before)
		}
		rec := h.audit.records[before]
		if rec.Action != mutation.action || rec.Outcome != "success" || rec.ErrorCode != "" {
			t.Fatalf("%s: record = %+v", mutation.action, rec)
		}
		if rec.RequestID != w.Header().Get(adminv1.HeaderRequestID) || !rec.At.Equal(testStart) || rec.RemoteAddr == "" {
			t.Fatalf("%s: record = %+v", mutation.action, rec)
		}
	}
	t.Run("authenticated mutations carry the calling device as actor", func(t *testing.T) {
		for _, rec := range h.audit.records {
			if strings.HasPrefix(rec.Action, "bucket.") || strings.HasPrefix(rec.Action, "object.") || strings.HasPrefix(rec.Action, "access_key.") {
				if rec.Actor.Type != "device" || rec.Actor.DeviceID != h.device.DeviceID || rec.Actor.DeviceName != h.device.Name {
					t.Fatalf("%s actor = %+v", rec.Action, rec.Actor)
				}
			}
		}
	})
	t.Run("reads are not audited", func(t *testing.T) {
		before := len(h.audit.records)
		for _, path := range []string{adminv1.PathBuckets, adminv1.PathStatus, adminv1.PathUsage, adminv1.PathAudit, adminv1.PathAccessKeys, adminv1.PathDevices, adminv1.PathSetupTelegram, "/admin/v1/buckets/photos/objects", "/admin/v1/buckets/photos/object/meta?key=a", "/admin/v1/buckets/photos/object?key=a"} {
			h.do(request{method: http.MethodGet, path: path})
		}
		if len(h.audit.records) != before {
			t.Fatalf("reads wrote %d audit records", len(h.audit.records)-before)
		}
	})
	t.Run("failed mutations are audited with the code", func(t *testing.T) {
		h := newHarness(t)
		h.buckets.err = errs.ErrBucketExists
		h.do(request{method: http.MethodPost, path: adminv1.PathBuckets, body: adminv1.CreateBucketRequest{Name: "photos"}})
		if len(h.audit.records) != 1 || h.audit.records[0].Outcome != "failure" || h.audit.records[0].ErrorCode != "bucket_exists" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("audit store failure does not fail the request", func(t *testing.T) {
		h := newHarness(t)
		h.audit.err = errors.New("db down")
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/buckets/photos"})
		if w.Code != 204 {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("no secret reaches any record", func(t *testing.T) {
		for _, rec := range h.audit.records {
			joined := rec.Action + rec.Target + rec.ErrorCode + rec.RemoteAddr + rec.Actor.DeviceName
			for _, secret := range []string{"SECRETVALUE", h.keys.secret, strings.Repeat("a", 32), "+447700900134", "bdt1_"} {
				if strings.Contains(joined, secret) {
					t.Fatalf("record leaks %q: %+v", secret, rec)
				}
			}
		}
	})
}
