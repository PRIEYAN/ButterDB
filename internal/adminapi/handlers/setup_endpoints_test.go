package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

func TestUnknownFieldRejected(t *testing.T) {
	routes := []struct {
		name string
		path string
		body string
	}{
		{name: "claim", path: adminv1.PathSetupClaim, body: `{"setup_token":"t","device_name":"d","platform":"linux/amd64","client_version":"1","extra":1}`},
		{name: "pairing request", path: adminv1.PathPairingRequest, body: `{"delivery":"saved_messages","device_name":"d","platform":"p","nope":true}`},
		{name: "telegram app", path: adminv1.PathSetupTelegramApp, body: `{"api_id":1,"api_hash":"x","other":2}`},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: http.MethodPost, path: route.path, raw: route.body})
			body := assertError(t, w, 400, adminv1.CodeBadRequest)
			if field := body.Details["field"]; field == "" || field == nil {
				t.Fatalf("details = %v, want the offending field", body.Details)
			}
		})
	}
	t.Run("field name is reported", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, raw: `{"surprise":1}`})
		if got := assertError(t, w, 400, adminv1.CodeBadRequest).Details["field"]; got != "surprise" {
			t.Fatalf("field = %v, want surprise", got)
		}
	})
}

func TestJSONBodyLimit(t *testing.T) {
	t.Run("over one mebibyte", func(t *testing.T) {
		h := newHarness(t)
		padding := strings.Repeat("a", 1<<20)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, raw: `{"setup_token":"` + padding + `"}`})
		body := assertError(t, w, 413, adminv1.CodePayloadTooLarge)
		if limit, ok := body.Details["limit_bytes"].(float64); !ok || int64(limit) != 1<<20 {
			t.Fatalf("details = %v", body.Details)
		}
	})
	t.Run("exactly at the limit is read", func(t *testing.T) {
		h := newHarness(t)
		prefix := `{"setup_token":"`
		suffix := `"}`
		padding := strings.Repeat("a", (1<<20)-len(prefix)-len(suffix))
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, raw: prefix + padding + suffix})
		assertError(t, w, 422, adminv1.CodeValidationFailed)
	})
}

func TestJSONContentRules(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		headers map[string]string
		status  int
		code    adminv1.Code
	}{
		{name: "wrong content type", raw: `{}`, headers: map[string]string{"Content-Type": "text/plain"}, status: 415, code: adminv1.CodeUnsupportedMediaType},
		{name: "content type with charset", raw: `{"setup_token":"","device_name":"d"}`, headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, status: 422, code: adminv1.CodeValidationFailed},
		{name: "malformed json", raw: `{"setup_token":`, status: 400, code: adminv1.CodeBadRequest},
		{name: "empty body", raw: "", status: 400, code: adminv1.CodeBadRequest},
		{name: "trailing document", raw: `{} {}`, status: 400, code: adminv1.CodeBadRequest},
		{name: "wrong field type", raw: `{"setup_token":5}`, status: 400, code: adminv1.CodeBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			headers := map[string]string{"Content-Type": "application/json"}
			for k, v := range test.headers {
				headers[k] = v
			}
			r := request{method: http.MethodPost, path: adminv1.PathSetupClaim, raw: test.raw, headers: headers}
			if test.raw == "" {
				r.reader = strings.NewReader("")
				r.length = 0
			}
			w := h.do(r)
			assertError(t, w, test.status, test.code)
		})
	}
}

func TestServerInfo(t *testing.T) {
	h := newHarness(t)
	h.setup.info = adminv1.ServerInfo{ServerID: "srv", APIVersion: "v1", State: "unclaimed"}
	w := h.do(request{method: http.MethodGet, path: adminv1.PathServerInfo})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	info := decodeBody[adminv1.ServerInfo](t, w)
	if info.ServerID != "srv" || !info.Time.Equal(testStart) {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(w.Body.String(), `"pairing_methods":[]`) {
		t.Fatalf("pairing_methods must be an empty array: %s", w.Body.String())
	}
	h.setup.err = errors.New("db down")
	assertError(t, h.do(request{method: http.MethodGet, path: adminv1.PathServerInfo}), 500, adminv1.CodeInternal)
}

func claimBody() map[string]string {
	return map[string]string{"setup_token": "bdst1_SECRETVALUE", "device_name": "Office laptop", "platform": "windows/amd64", "client_version": "0.1.0"}
}

func TestClaimEndpoint(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newHarness(t)
		h.setup.result = adminv1.ClaimResult{
			Device:      adminv1.Device{DeviceID: "dev1", Name: "Office laptop", Current: true},
			DeviceToken: "bdt1_dev1_secret",
			ServerID:    "srv",
		}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, body: claimBody()})
		if w.Code != 201 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := decodeBody[adminv1.ClaimResult](t, w); got.DeviceToken != "bdt1_dev1_secret" || got.ServerID != "srv" {
			t.Fatalf("result = %+v", got)
		}
		if h.setup.token != "bdst1_SECRETVALUE" || h.setup.device.Name != "Office laptop" || h.setup.device.ClientVersion != "0.1.0" {
			t.Fatalf("claim got token %q device %+v", h.setup.token, h.setup.device)
		}
		if h.setup.device.RemoteAddr == "" {
			t.Fatal("claim got no remote address")
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Action != "setup.claim" || h.audit.records[0].Outcome != "success" || h.audit.records[0].Actor.DeviceID != "dev1" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	failures := []struct {
		name   string
		err    error
		status int
		code   adminv1.Code
	}{
		{name: "already claimed", err: ErrAlreadyClaimed, status: 409, code: adminv1.CodeAlreadyClaimed},
		{name: "token invalid", err: ErrSetupTokenInvalid, status: 401, code: adminv1.CodeSetupTokenInvalid},
		{name: "rate limited", err: &RateLimitedError{Scope: "claim", RetryAfter: time.Minute}, status: 429, code: adminv1.CodeRateLimited},
	}
	for _, test := range failures {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.setup.err = test.err
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, body: claimBody()})
			assertError(t, w, test.status, test.code)
			if len(h.audit.records) != 1 || h.audit.records[0].Outcome != "failure" || h.audit.records[0].ErrorCode != string(test.code) {
				t.Fatalf("audit = %+v", h.audit.records)
			}
		})
	}
	validation := []struct {
		name   string
		mutate func(m map[string]string)
		field  string
	}{
		{name: "empty token", mutate: func(m map[string]string) { m["setup_token"] = "  " }, field: "setup_token"},
		{name: "empty device name", mutate: func(m map[string]string) { m["device_name"] = "" }, field: "device_name"},
		{name: "long device name", mutate: func(m map[string]string) { m["device_name"] = strings.Repeat("é", 65) }, field: "device_name"},
		{name: "control character", mutate: func(m map[string]string) { m["device_name"] = "bad\x07name" }, field: "device_name"},
		{name: "long client version", mutate: func(m map[string]string) { m["client_version"] = strings.Repeat("1", 33) }, field: "client_version"},
	}
	for _, test := range validation {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			body := claimBody()
			test.mutate(body)
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, body: body})
			got := assertError(t, w, 422, adminv1.CodeValidationFailed)
			if got.Details["field"] != test.field {
				t.Fatalf("details = %v, want field %s", got.Details, test.field)
			}
			if h.setup.token != "" {
				t.Fatal("claim reached the service despite invalid input")
			}
		})
	}
	t.Run("setup token never reaches the audit log", func(t *testing.T) {
		h := newHarness(t)
		h.setup.err = ErrSetupTokenInvalid
		h.do(request{method: http.MethodPost, path: adminv1.PathSetupClaim, body: claimBody()})
		for _, rec := range h.audit.records {
			if strings.Contains(rec.Target+rec.Action+rec.ErrorCode, "SECRETVALUE") {
				t.Fatalf("audit record leaks the token: %+v", rec)
			}
		}
	})
}

func TestPairingEndpoints(t *testing.T) {
	t.Run("request success", func(t *testing.T) {
		h := newHarness(t)
		h.pairing.expires = testStart.Add(10 * time.Minute)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingRequest, body: adminv1.PairingRequest{Delivery: "saved_messages", DeviceName: "Home", Platform: "linux/amd64"}})
		if w.Code != 202 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := decodeBody[adminv1.PairingRequested](t, w); got.Delivery != "saved_messages" || !got.ExpiresAt.Equal(h.pairing.expires) {
			t.Fatalf("body = %+v", got)
		}
		if h.pairing.device.Name != "Home" || len(h.audit.records) != 1 || h.audit.records[0].Action != "pairing.request" {
			t.Fatalf("device %+v audit %+v", h.pairing.device, h.audit.records)
		}
	})
	t.Run("host delivery is not requestable", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingRequest, body: adminv1.PairingRequest{Delivery: "host", DeviceName: "Home"}})
		assertError(t, w, 422, adminv1.CodeValidationFailed)
	})
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   adminv1.Code
	}{
		{"not claimed", ErrNotClaimed, 409, adminv1.CodeNotClaimed},
		{"unavailable", ErrPairingUnavailable, 409, adminv1.CodePairingUnavailable},
		{"telegram unavailable", identity.ErrTelegramUnavailable, 503, adminv1.CodeTelegramUnavailable},
	} {
		t.Run("request "+test.name, func(t *testing.T) {
			h := newHarness(t)
			h.pairing.err = test.err
			w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingRequest, body: adminv1.PairingRequest{Delivery: "saved_messages", DeviceName: "Home"}})
			assertError(t, w, test.status, test.code)
		})
	}
	t.Run("complete success", func(t *testing.T) {
		h := newHarness(t)
		h.pairing.result = adminv1.DeviceTokenResult{Device: adminv1.Device{DeviceID: "dev2", Name: "Home"}, DeviceToken: "bdt1_dev2_x", ServerID: "srv"}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingComplete, body: adminv1.PairingCompleteRequest{Code: " ABCDE-FGHJK ", DeviceName: "Home", Platform: "linux/amd64", ClientVersion: "0.1.0"}})
		if w.Code != 201 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if h.pairing.code != "ABCDE-FGHJK" {
			t.Fatalf("code = %q", h.pairing.code)
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Action != "pairing.complete" || h.audit.records[0].Target != "dev2" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("complete invalid code", func(t *testing.T) {
		h := newHarness(t)
		h.pairing.err = ErrPairingCodeInvalid
		w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingComplete, body: adminv1.PairingCompleteRequest{Code: "WRONG", DeviceName: "Home"}})
		assertError(t, w, 401, adminv1.CodePairingCodeInvalid)
		if h.audit.records[0].Outcome != "failure" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("complete empty code", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPost, path: adminv1.PathPairingComplete, body: adminv1.PairingCompleteRequest{Code: " ", DeviceName: "Home"}})
		assertError(t, w, 422, adminv1.CodeValidationFailed)
	})
}

func TestUnauthenticatedRoutesAreExactlyFour(t *testing.T) {
	h := newHarness(t)
	h.reject = true
	h.setup.result = adminv1.ClaimResult{Device: adminv1.Device{DeviceID: "d"}}
	open := []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodGet, adminv1.PathServerInfo, nil, 200},
		{http.MethodPost, adminv1.PathSetupClaim, claimBody(), 201},
		{http.MethodPost, adminv1.PathPairingRequest, adminv1.PairingRequest{Delivery: "saved_messages", DeviceName: "d"}, 202},
		{http.MethodPost, adminv1.PathPairingComplete, adminv1.PairingCompleteRequest{Code: "c", DeviceName: "d"}, 201},
	}
	for _, route := range open {
		if w := h.do(request{method: route.method, path: route.path, body: route.body}); w.Code != route.want {
			t.Errorf("%s %s = %d, want %d", route.method, route.path, w.Code, route.want)
		}
	}
	guarded := []struct{ method, path string }{
		{http.MethodGet, adminv1.PathDevices},
		{http.MethodPatch, "/admin/v1/devices/x"},
		{http.MethodDelete, "/admin/v1/devices/x"},
		{http.MethodPost, adminv1.PathDeviceSelfRotate},
		{http.MethodGet, adminv1.PathSetupTelegram},
		{http.MethodPost, adminv1.PathSetupTelegramApp},
		{http.MethodDelete, adminv1.PathSetupTelegramApp},
		{http.MethodPost, adminv1.PathSetupTelegramPhone},
		{http.MethodPost, adminv1.PathSetupTelegramCode},
		{http.MethodPost, adminv1.PathSetupTelegramPassword},
		{http.MethodPost, adminv1.PathSetupTelegramCancel},
		{http.MethodPost, adminv1.PathSetupTelegramUnlink},
		{http.MethodGet, adminv1.PathReplica},
	}
	for _, route := range guarded {
		if w := h.do(request{method: route.method, path: route.path}); w.Code != 401 {
			t.Errorf("%s %s = %d, want 401 from the auth middleware", route.method, route.path, w.Code)
		}
	}
}

func deviceFixture(id, name string) adminrepo.Device {
	return adminrepo.Device{
		DeviceID:      id,
		Name:          name,
		Platform:      "linux/amd64",
		CreatedVia:    adminrepo.CreatedViaSetup,
		CreatedAt:     testStart,
		LastSeenAt:    testStart,
		TokenIssuedAt: testStart,
	}
}

func TestDeviceEndpoints(t *testing.T) {
	t.Run("list marks the current device and omits secrets", func(t *testing.T) {
		h := newHarness(t)
		revoked := deviceFixture("devB", "Old phone")
		revoked.RevokedAt = testStart.Add(-time.Hour)
		revoked.TokenHash = [32]byte{1}
		h.devices.devices = []adminrepo.Device{deviceFixture(h.device.DeviceID, "Office laptop"), revoked}
		w := h.do(request{method: http.MethodGet, path: adminv1.PathDevices})
		if w.Code != 200 {
			t.Fatalf("status = %d", w.Code)
		}
		list := decodeBody[adminv1.DeviceList](t, w)
		if len(list.Devices) != 2 || !list.Devices[0].Current || list.Devices[1].Current {
			t.Fatalf("devices = %+v", list.Devices)
		}
		if list.Devices[0].RevokedAt != nil || list.Devices[1].RevokedAt == nil {
			t.Fatalf("revoked_at = %v / %v", list.Devices[0].RevokedAt, list.Devices[1].RevokedAt)
		}
		if strings.Contains(w.Body.String(), "token") && !strings.Contains(w.Body.String(), "token_issued_at") {
			t.Fatalf("response mentions a token field: %s", w.Body.String())
		}
	})
	t.Run("rename", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPatch, path: "/admin/v1/devices/devX", body: adminv1.UpdateDeviceRequest{Name: "New name"}})
		if w.Code != 200 || h.devices.renamed["devX"] != "New name" {
			t.Fatalf("status %d renamed %v", w.Code, h.devices.renamed)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "device.rename" {
			t.Fatalf("audit = %v", got)
		}
	})
	t.Run("rename validates the name", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodPatch, path: "/admin/v1/devices/devX", body: adminv1.UpdateDeviceRequest{Name: ""}})
		if got := assertError(t, w, 422, adminv1.CodeValidationFailed).Details["field"]; got != "name" {
			t.Fatalf("field = %v", got)
		}
	})
	t.Run("rename unknown device", func(t *testing.T) {
		h := newHarness(t)
		h.devices.err = ErrDeviceNotFound
		w := h.do(request{method: http.MethodPatch, path: "/admin/v1/devices/nope", body: adminv1.UpdateDeviceRequest{Name: "x"}})
		assertError(t, w, 404, adminv1.CodeDeviceNotFound)
	})
	t.Run("revoke passes confirm_last", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/devices/devX?confirm_last=true"})
		if w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("status %d body %q", w.Code, w.Body.String())
		}
		if len(h.devices.confirmLast) != 1 || !h.devices.confirmLast[0] || h.devices.revoked[0] != "devX" {
			t.Fatalf("revoke args = %v %v", h.devices.revoked, h.devices.confirmLast)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "device.revoke" {
			t.Fatalf("audit = %v", got)
		}
	})
	t.Run("revoke the last device without confirmation", func(t *testing.T) {
		h := newHarness(t)
		h.devices.err = ErrLastDevice
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/devices/devX"})
		assertError(t, w, 409, adminv1.CodeLastDevice)
		if h.devices.confirmLast[0] {
			t.Fatal("confirm_last defaulted to true")
		}
	})
	t.Run("revoke bad confirm_last", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(request{method: http.MethodDelete, path: "/admin/v1/devices/devX?confirm_last=maybe"})
		assertError(t, w, 400, adminv1.CodeBadRequest)
	})
	t.Run("rotate returns credentials for the calling device", func(t *testing.T) {
		h := newHarness(t)
		h.setup.info = adminv1.ServerInfo{ServerID: "srv"}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathDeviceSelfRotate})
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		got := decodeBody[adminv1.DeviceCredentials](t, w)
		if got.DeviceToken != "bdt1_rotated" || got.ServerID != "srv" || !got.Device.Current || h.devices.rotated[0] != h.device.DeviceID {
			t.Fatalf("credentials = %+v rotated %v", got, h.devices.rotated)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response is cacheable")
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "device.rotate" {
			t.Fatalf("audit = %v", got)
		}
	})
}
