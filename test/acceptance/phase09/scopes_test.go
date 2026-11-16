package phase09

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
)

type rawResponse struct {
	status int
	header http.Header
	body   []byte
}

func (w *world) raw(method, path, token string, body io.Reader) rawResponse {
	w.t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(method, w.baseURL+path, body)
	if err != nil {
		w.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return rawResponse{status: resp.StatusCode, header: resp.Header, body: data}
}

func (r rawResponse) errorBody(t *testing.T) adminv1.ErrorBody {
	t.Helper()
	var envelope adminv1.ErrorEnvelope
	if err := json.Unmarshal(r.body, &envelope); err != nil {
		t.Fatalf("response %q is not an error envelope: %v", r.body, err)
	}
	return envelope.Error
}

func allAuditEvents(t *testing.T, client *adminv1.Client, limit int) []adminv1.AuditEvent {
	t.Helper()
	var events []adminv1.AuditEvent
	cursor := ""
	for page := 0; page < 100; page++ {
		got, err := client.Audit(context.Background(), adminv1.AuditQuery{Cursor: cursor, Limit: limit})
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		events = append(events, got.Events...)
		if got.NextCursor == "" {
			return events
		}
		cursor = got.NextCursor
	}
	t.Fatal("audit paging never ended")
	return nil
}

func TestAdminTokenScopesAndAuditEventsAreEnforced(t *testing.T) {
	ctx := context.Background()

	protected := []struct {
		name   string
		method string
		path   string
	}{
		{"status", http.MethodGet, adminv1.PathStatus},
		{"usage", http.MethodGet, adminv1.PathUsage},
		{"audit", http.MethodGet, adminv1.PathAudit},
		{"devices", http.MethodGet, adminv1.PathDevices},
		{"rotate", http.MethodPost, adminv1.PathDeviceSelfRotate},
		{"telegram status", http.MethodGet, adminv1.PathSetupTelegram},
		{"buckets", http.MethodGet, adminv1.PathBuckets},
		{"bucket create", http.MethodPost, adminv1.PathBuckets},
		{"bucket objects", http.MethodGet, "/admin/v1/buckets/photos/objects"},
		{"object upload", http.MethodPut, "/admin/v1/buckets/photos/object?key=a"},
		{"access keys", http.MethodGet, adminv1.PathAccessKeys},
	}

	t.Run("every protected route demands a device token and the 401 carries the request id", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, creds := w.claim()
		for _, route := range protected {
			resp := w.raw(route.method, route.path, "", nil)
			if resp.status != http.StatusUnauthorized {
				t.Fatalf("%s without a token: status %d", route.name, resp.status)
			}
			body := resp.errorBody(t)
			if body.Code != adminv1.CodeUnauthenticated || body.RequestID == "" || body.RequestID != resp.header.Get(adminv1.HeaderRequestID) {
				t.Fatalf("%s without a token: body %+v, request id header %q", route.name, body, resp.header.Get(adminv1.HeaderRequestID))
			}
		}
		if _, err := owner.ListDevices(ctx); err != nil {
			t.Fatalf("the owner's token stopped working: %v", err)
		}
		for name, token := range map[string]string{
			"garbage":              "not-a-token",
			"setup token":          w.setupToken(),
			"well formed unknown":  "bdt1_" + repeat("a", 16) + "_" + repeat("b", 52),
			"right id wrong token": "bdt1_" + creds.Device.DeviceID + "_" + repeat("c", 52),
		} {
			resp := w.raw(http.MethodGet, adminv1.PathDevices, token, nil)
			if resp.status != http.StatusUnauthorized || resp.errorBody(t).Code != adminv1.CodeUnauthenticated {
				t.Fatalf("%s as bearer: status %d body %s", name, resp.status, resp.body)
			}
		}
	})

	t.Run("the four public routes need no token", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		if resp := w.raw(http.MethodGet, adminv1.PathServerInfo, "", nil); resp.status != http.StatusOK {
			t.Fatalf("server-info status %d", resp.status)
		}
		for _, path := range []string{adminv1.PathSetupClaim, adminv1.PathPairingRequest, adminv1.PathPairingComplete} {
			resp := w.raw(http.MethodPost, path, "", strings.NewReader(`{}`))
			if resp.status == http.StatusUnauthorized && resp.errorBody(t).Code == adminv1.CodeUnauthenticated {
				t.Fatalf("%s demanded a device token", path)
			}
		}
	})

	t.Run("a revoked device is rejected on its next request", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, _ := w.claim()
		_, hostCode, _, err := session.RequestAdminToken(ctx, w.cfg.Admin.ControlSocket)
		if err != nil {
			t.Fatalf("host code: %v", err)
		}
		second, err := w.client("").CompletePairing(ctx, completeRequest(hostCode))
		if err != nil {
			t.Fatalf("pair second device: %v", err)
		}
		secondClient := w.client(second.DeviceToken)
		if _, err := secondClient.ListDevices(ctx); err != nil {
			t.Fatalf("second device: %v", err)
		}
		if err := owner.RevokeDevice(ctx, second.Device.DeviceID, false); err != nil {
			t.Fatalf("revoke second device: %v", err)
		}
		_, err = secondClient.ListDevices(ctx)
		requireCode(t, err, adminv1.CodeDeviceRevoked)
		list, err := owner.ListDevices(ctx)
		if err != nil || len(list.Devices) != 2 {
			t.Fatalf("device list = %+v, %v", list, err)
		}
	})

	t.Run("the last device needs explicit confirmation to be revoked", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, creds := w.claim()
		requireCode(t, owner.RevokeDevice(ctx, creds.Device.DeviceID, false), adminv1.CodeLastDevice)
		if err := owner.RevokeDevice(ctx, creds.Device.DeviceID, true); err != nil {
			t.Fatalf("confirmed revoke: %v", err)
		}
		_, err := owner.ListDevices(ctx)
		requireCode(t, err, adminv1.CodeDeviceRevoked)
		info, err := w.client("").ServerInfo(ctx)
		if err != nil || info.State != "claimed" {
			t.Fatalf("a revoked last device must leave the server claimed: %+v, %v", info, err)
		}
	})

	t.Run("rotation hands out a working token and retires the old one on first use", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, creds := w.claim()
		rotated, err := owner.RotateDeviceToken(ctx)
		if err != nil {
			t.Fatalf("RotateDeviceToken: %v", err)
		}
		if rotated.DeviceToken == creds.DeviceToken || rotated.Device.DeviceID != creds.Device.DeviceID || rotated.ServerID != w.serverID {
			t.Fatalf("rotation result = %+v", rotated)
		}
		if !strings.HasPrefix(rotated.DeviceToken, "bdt1_"+creds.Device.DeviceID+"_") {
			t.Fatalf("rotated token %q does not keep the device id", rotated.DeviceToken)
		}
		fresh := w.client(rotated.DeviceToken)
		if _, err := fresh.ListDevices(ctx); err != nil {
			t.Fatalf("new token: %v", err)
		}
		_, err = owner.ListDevices(ctx)
		requireCode(t, err, adminv1.CodeUnauthenticated)
	})

	t.Run("a device token is not an S3 credential", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, creds := w.claim()
		w.linkTelegram(owner)
		backend := w.publishBackend()
		req, err := http.NewRequest(http.MethodGet, backend.srv.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+creds.DeviceToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Fatalf("S3 listener with a device token: status %d, want a 4xx refusal", resp.StatusCode)
		}
	})

	t.Run("failed authentication is audited at most once a minute per address", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, _ := w.claim()
		for range 5 {
			w.raw(http.MethodGet, adminv1.PathDevices, "bdt1_"+repeat("a", 16)+"_"+repeat("b", 52), nil)
		}
		failures := func() int {
			count := 0
			for _, event := range allAuditEvents(t, owner, 100) {
				if event.Action == "auth.failure" {
					count++
					if event.Outcome != "failure" || event.Actor.Type != "anonymous" || event.RemoteAddr != "127.0.0.1" || event.RequestID == "" {
						t.Fatalf("auth.failure event = %+v", event)
					}
				}
			}
			return count
		}
		if got := failures(); got != 1 {
			t.Fatalf("%d auth.failure events after five bad requests inside one minute, want 1", got)
		}
		w.clk.Advance(61 * time.Second)
		w.raw(http.MethodGet, adminv1.PathDevices, "bdt1_"+repeat("a", 16)+"_"+repeat("b", 52), nil)
		if got := failures(); got != 2 {
			t.Fatalf("%d auth.failure events after a bad request a minute later, want 2", got)
		}
	})

	t.Run("mutations are audited without secrets and the log pages", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, creds := w.claim()
		w.linkTelegram(owner)
		w.publishBackend()

		if _, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "photos"}); err != nil {
			t.Fatalf("CreateBucket: %v", err)
		}
		_, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "photos"})
		requireCode(t, err, adminv1.CodeBucketExists)
		key, err := owner.CreateAccessKey(ctx, adminv1.CreateAccessKeyRequest{Name: "ci", Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read", "list"}}})
		if err != nil {
			t.Fatalf("CreateAccessKey: %v", err)
		}
		if _, err := owner.RevokeAccessKey(ctx, key.AccessKey.AccessKeyID); err != nil {
			t.Fatalf("RevokeAccessKey: %v", err)
		}
		if _, err := owner.RenameDevice(ctx, creds.Device.DeviceID, adminv1.UpdateDeviceRequest{Name: "Renamed laptop"}); err != nil {
			t.Fatalf("RenameDevice: %v", err)
		}

		events := allAuditEvents(t, owner, 3)
		if unpaged := allAuditEvents(t, owner, 500); len(unpaged) != len(events) {
			t.Fatalf("paged %d events, unpaged %d", len(events), len(unpaged))
		}
		seen := map[string]adminv1.AuditEvent{}
		for _, event := range events {
			if _, dup := seen[event.Action+"|"+event.Outcome]; !dup {
				seen[event.Action+"|"+event.Outcome] = event
			}
		}
		for _, want := range []string{
			"setup.claim|success", "telegram.credentials_save|success", "telegram.login_start|success", "telegram.login_failure|failure",
			"telegram.login_success|success", "bucket.create|success", "bucket.create|failure", "access_key.create|success",
			"access_key.revoke|success", "device.rename|success",
		} {
			if _, ok := seen[want]; !ok {
				t.Fatalf("audit log lacks %s; has %v", want, keysOf(seen))
			}
		}
		if failed := seen["bucket.create|failure"]; failed.ErrorCode != string(adminv1.CodeBucketExists) || failed.Target != "photos" {
			t.Fatalf("failed bucket.create event = %+v", failed)
		}
		if created := seen["bucket.create|success"]; created.Actor.Type != "device" || created.Actor.DeviceID != creds.Device.DeviceID || created.RemoteAddr != "127.0.0.1" || created.RequestID == "" {
			t.Fatalf("bucket.create event = %+v", created)
		}
		for i := 1; i < len(events); i++ {
			if events[i].At.After(events[i-1].At) {
				t.Fatalf("audit events are not newest first at %d: %v then %v", i, events[i-1].At, events[i].At)
			}
		}
		filtered, err := owner.Audit(ctx, adminv1.AuditQuery{Action: "bucket."})
		if err != nil || len(filtered.Events) != 2 {
			t.Fatalf("audit filtered by bucket. = %+v, %v", filtered, err)
		}

		dump, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		for name, secret := range map[string]string{
			"device token": creds.DeviceToken, "access key secret": key.SecretAccessKey, "telegram api hash": telegramHash,
			"login password": loginPass, "login code": loginCode, "phone number": phoneNumber,
		} {
			if bytes.Contains(dump, []byte(secret)) {
				t.Fatalf("the audit log contains the %s", name)
			}
		}
		if bytes.Contains(dump, []byte(w.setupToken())) {
			t.Fatal("the audit log contains the setup token")
		}
	})
}

func keysOf(m map[string]adminv1.AuditEvent) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}
