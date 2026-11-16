package vm

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func TestRevokeLastDeviceAsksTwice(t *testing.T) {
	h := newHarness(t)
	h.client.revokeErrs = []error{apiError(adminv1.CodeLastDevice), nil}
	ctx := context.Background()
	h.vm.Refresh(ctx)

	first := h.vm.Revoke(ctx, testDeviceID, false)
	if first != RevokeNeedsLastConfirmation {
		t.Fatalf("first outcome = %v, want RevokeNeedsLastConfirmation", first)
	}
	if st := h.vm.State().Get(); st.Problem != nil {
		t.Fatalf("last_device must not surface as a problem: %+v", st.Problem)
	}
	if len(h.tokens.deletes) != 0 {
		t.Fatalf("token deleted before the second confirmation")
	}

	second := h.vm.Revoke(ctx, testDeviceID, true)
	if second != RevokedThisDevice {
		t.Fatalf("second outcome = %v, want RevokedThisDevice", second)
	}
	want := []revokeCall{{testDeviceID, false}, {testDeviceID, true}}
	if len(h.client.revokeCalls) != 2 || h.client.revokeCalls[0] != want[0] || h.client.revokeCalls[1] != want[1] {
		t.Fatalf("revoke calls = %+v, want %+v", h.client.revokeCalls, want)
	}
}

func TestRevokeConfirmedLastDeviceStillFailsOnRepeat(t *testing.T) {
	h := newHarness(t)
	h.client.revokeErrs = []error{apiError(adminv1.CodeLastDevice)}
	got := h.vm.Revoke(context.Background(), "dev-other", true)
	if got != RevokeFailed {
		t.Fatalf("outcome = %v", got)
	}
	if p := h.vm.State().Get().Problem; p == nil || p.Code != adminv1.CodeLastDevice || p.Presentation != model.PresentDialog {
		t.Fatalf("Problem = %+v", p)
	}
}

func TestRevokeSelfRoutesToConnect(t *testing.T) {
	tests := []struct {
		name     string
		deviceID string
		listed   bool
		want     RevokeOutcome
		route    model.Route
		forget   bool
	}{
		{"own device from list", testDeviceID, true, RevokedThisDevice, model.RouteConnect, true},
		{"own device by profile id before list", testDeviceID, false, RevokedThisDevice, model.RouteConnect, true},
		{"another device keeps session", "dev-other", true, RevokeDone, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if tt.listed {
				h.vm.RefreshDevices(ctx)
			}
			got := h.vm.Revoke(ctx, tt.deviceID, false)
			if got != tt.want || got.Route() != tt.route {
				t.Fatalf("outcome = %v route = %q", got, got.Route())
			}
			_, tokenErr := h.tokens.Get(testServerID)
			_, profileErr := h.profiles.Get(testServerID)
			if tt.forget && (!errors.Is(tokenErr, model.ErrTokenNotFound) || !errors.Is(profileErr, model.ErrProfileNotFound)) {
				t.Fatalf("token err = %v profile err = %v, want both removed", tokenErr, profileErr)
			}
			if !tt.forget && (tokenErr != nil || profileErr != nil) {
				t.Fatalf("session removed for another device: %v %v", tokenErr, profileErr)
			}
		})
	}
}

func TestRevokeDeviceNotFoundRefreshesList(t *testing.T) {
	h := newHarness(t)
	h.client.revokeErrs = []error{apiError(adminv1.CodeDeviceNotFound)}
	got := h.vm.Revoke(context.Background(), "dev-gone", false)
	st := h.vm.State().Get()
	if got != RevokeFailed || st.Problem == nil || st.Problem.Code != adminv1.CodeDeviceNotFound {
		t.Fatalf("outcome %v problem %+v", got, st.Problem)
	}
	if !st.Devices.Loaded || len(st.Devices.Rows) != 2 {
		t.Fatalf("devices not refreshed: %+v", st.Devices)
	}
}

func TestUnlinkRequiresTypedWord(t *testing.T) {
	tests := []struct {
		name      string
		typed     string
		want      UnlinkOutcome
		wantCalls int
	}{
		{"empty is refused", "", UnlinkNotConfirmed, 0},
		{"wrong case is refused", "Unlink", UnlinkNotConfirmed, 0},
		{"padded is refused", " unlink", UnlinkNotConfirmed, 0},
		{"other word is refused", "yes", UnlinkNotConfirmed, 0},
		{"exact word unlinks", "unlink", UnlinkDone, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			got := h.vm.Unlink(context.Background(), tt.typed)
			if got != tt.want || h.client.unlinks != tt.wantCalls {
				t.Fatalf("outcome %v calls %d", got, h.client.unlinks)
			}
			if tt.want == UnlinkDone {
				if got.Route() != model.RouteTelegram {
					t.Fatalf("route = %q", got.Route())
				}
				if st := h.vm.State().Get().Telegram; st.Linked() || st.Status.State != adminv1.TelegramReady {
					t.Fatalf("telegram state after unlink = %+v", st)
				}
			}
		})
	}
}

func TestUnlinkFailureReportsProblem(t *testing.T) {
	h := newHarness(t)
	h.client.unlinkErr = apiError(adminv1.CodeTelegramUnavailable)
	if got := h.vm.Unlink(context.Background(), UnlinkWord); got != UnlinkFailed {
		t.Fatalf("outcome = %v", got)
	}
	if p := h.vm.State().Get().Problem; p == nil || p.Code != adminv1.CodeTelegramUnavailable {
		t.Fatalf("Problem = %+v", p)
	}
}

func TestAuditFilterPrefix(t *testing.T) {
	events := func(actions ...string) adminv1.AuditPage {
		page := adminv1.AuditPage{}
		for i, a := range actions {
			page.Events = append(page.Events, adminv1.AuditEvent{ID: int64(i + 1), Action: a})
		}
		return page
	}
	tests := []struct {
		name       string
		filter     string
		page       adminv1.AuditPage
		wantAction string
		wantRows   []string
	}{
		{"no filter keeps everything", "", events("bucket.create", "device.rename"), "", []string{"bucket.create", "device.rename"}},
		{"prefix sent to the server", "bucket.", events("bucket.create", "bucket.delete"), "bucket.", []string{"bucket.create", "bucket.delete"}},
		{"server rows outside the prefix are dropped", "device.", events("device.rename", "bucket.create", "device.revoke"), "device.", []string{"device.rename", "device.revoke"}},
		{"filter is trimmed and lowered", " Telegram.", events("telegram.unlink", "telegram_x.y"), "telegram.", []string{"telegram.unlink"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.client.auditPages = []adminv1.AuditPage{tt.page}
			h.vm.SetAuditFilter(context.Background(), tt.filter)
			if len(h.client.audits) != 1 {
				t.Fatalf("audit calls = %d", len(h.client.audits))
			}
			q := h.client.audits[0]
			if q.Action != tt.wantAction || q.Limit != AuditPageSize || q.Cursor != "" {
				t.Fatalf("query = %+v", q)
			}
			var got []string
			for _, e := range h.vm.State().Get().Activity.Events {
				got = append(got, e.Action)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantRows, ",") {
				t.Fatalf("rows = %v, want %v", got, tt.wantRows)
			}
		})
	}
}

func TestAuditPagingAndFilterReset(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.client.auditPages = []adminv1.AuditPage{
		{Events: []adminv1.AuditEvent{{ID: 3, Action: "bucket.create"}}, NextCursor: "c1"},
		{Events: []adminv1.AuditEvent{{ID: 2, Action: "bucket.update"}}},
		{Events: []adminv1.AuditEvent{{ID: 9, Action: "device.rename"}}},
	}
	h.vm.SetAuditFilter(ctx, "bucket.")
	if !h.vm.State().Get().Activity.HasMore() {
		t.Fatalf("HasMore = false")
	}
	h.vm.LoadMoreActivity(ctx)
	st := h.vm.State().Get().Activity
	if len(st.Events) != 2 || st.HasMore() || h.client.audits[1].Cursor != "c1" || h.client.audits[1].Action != "bucket." {
		t.Fatalf("after load more: %+v / %+v", st, h.client.audits)
	}
	h.vm.LoadMoreActivity(ctx)
	if len(h.client.audits) != 2 {
		t.Fatalf("load more without cursor called the server")
	}
	h.vm.SetAuditFilter(ctx, "device.")
	st = h.vm.State().Get().Activity
	if len(st.Events) != 1 || st.Events[0].Action != "device.rename" || st.Filter != "device." {
		t.Fatalf("after filter change: %+v", st)
	}
}

func TestAuditHelpers(t *testing.T) {
	if got := AuditFilters(); got[0] != "" || len(got) != 9 || got[8] != "replica." {
		t.Fatalf("filters = %v", got)
	}
	tests := []struct {
		name  string
		actor adminv1.AuditActor
		want  string
	}{
		{"device by name", adminv1.AuditActor{Type: "device", DeviceID: "d1", DeviceName: "Laptop"}, "Laptop"},
		{"device by id", adminv1.AuditActor{Type: "device", DeviceID: "d1"}, "d1"},
		{"host", adminv1.AuditActor{Type: "host"}, "host"},
		{"anonymous", adminv1.AuditActor{Type: "anonymous"}, "anonymous"},
		{"empty", adminv1.AuditActor{}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActorText(tt.actor); got != tt.want {
				t.Fatalf("ActorText = %q", got)
			}
		})
	}
}

func TestAuditFailureKeepsRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.client.auditPages = []adminv1.AuditPage{{Events: []adminv1.AuditEvent{{ID: 1, Action: "bucket.create"}}, NextCursor: "c"}}
	h.vm.RefreshActivity(ctx)
	h.client.auditErr = &net.OpError{Op: "dial", Err: errBoom}
	h.vm.LoadMoreActivity(ctx)
	st := h.vm.State().Get()
	if len(st.Activity.Events) != 1 || st.Problem == nil || !st.Problem.Retryable {
		t.Fatalf("state = %+v", st)
	}
}

func TestRotateStoresBeforeSwitching(t *testing.T) {
	h := newHarness(t)
	var order []string
	h.tokens.order, h.profiles.order = &order, &order
	h.vm = New(h.client, func() time.Time { return testNow }, Session{
		Profile: h.profile, Tokens: h.tokens, Profiles: h.profiles,
		SetClientToken: func(tok string) { order = append(order, "client.switch"); h.clientTk = append(h.clientTk, tok) },
	})
	issued := testNow.Add(-time.Second)
	h.client.rotate = adminv1.DeviceCredentials{DeviceToken: "new-token", Device: adminv1.Device{TokenIssuedAt: issued}}
	if !h.vm.RotateToken(context.Background()) {
		t.Fatalf("RotateToken = false, problem %+v", h.vm.State().Get().Problem)
	}
	if got := strings.Join(order, ","); got != "token.set,client.switch,profile.put" {
		t.Fatalf("order = %s", got)
	}
	if tok, _ := h.tokens.Get(testServerID); tok != "new-token" {
		t.Fatalf("stored token = %q", tok)
	}
	if h.profiles.profiles[testServerID].TokenIssuedAt != issued {
		t.Fatalf("token_issued_at = %v", h.profiles.profiles[testServerID].TokenIssuedAt)
	}
	st := h.vm.State().Get()
	if st.Notice != NoticeTokenRotated || st.Connection.Profile.TokenIssuedAt != issued {
		t.Fatalf("state = %+v", st)
	}
}

func TestRotateStoreFailureKeepsOldToken(t *testing.T) {
	h := newHarness(t)
	h.tokens.setErr = model.ErrKeychainUnavailable
	h.client.rotate = adminv1.DeviceCredentials{DeviceToken: "new-token"}
	if h.vm.RotateToken(context.Background()) {
		t.Fatalf("RotateToken = true despite store failure")
	}
	if len(h.clientTk) != 0 {
		t.Fatalf("client switched to %q despite failed store", h.clientTk)
	}
	if tok, _ := h.tokens.Get(testServerID); tok != "old-token" {
		t.Fatalf("stored token = %q, want old token kept", tok)
	}
	if h.profiles.profiles[testServerID].TokenIssuedAt != h.profile.TokenIssuedAt {
		t.Fatalf("profile updated despite failed store")
	}
	p := h.vm.State().Get().Problem
	if p == nil || p.Presentation != model.PresentBanner || p.Title != textRotatedNotSaved {
		t.Fatalf("Problem = %+v", p)
	}
	if strings.Contains(p.Title+p.Body, "new-token") {
		t.Fatalf("token leaked into problem text")
	}
}

func TestRotateServerFailure(t *testing.T) {
	h := newHarness(t)
	h.client.rotateErr = apiError(adminv1.CodeInternal)
	if h.vm.RotateToken(context.Background()) {
		t.Fatalf("RotateToken = true")
	}
	if tok, _ := h.tokens.Get(testServerID); tok != "old-token" || len(h.clientTk) != 0 {
		t.Fatalf("token changed on server failure")
	}
}

func TestRenameDevice(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		renameErr  error
		profileErr error
		wantCalls  int
		wantName   string
		wantField  string
	}{
		{"valid name is trimmed and saved", "  Work laptop ", nil, nil, 1, "Work laptop", ""},
		{"empty name is refused locally", "   ", nil, nil, 0, "Office laptop", "name"},
		{"too long is refused locally", strings.Repeat("x", 65), nil, nil, 0, "Office laptop", "name"},
		{"server error keeps the name", "Work laptop", apiError(adminv1.CodeValidationFailed), nil, 1, "Office laptop", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			h.vm.RefreshDevices(ctx)
			h.client.renameErr = tt.renameErr
			h.vm.RenameDevice(ctx, tt.input)
			if len(h.client.renames) != tt.wantCalls {
				t.Fatalf("rename calls = %v", h.client.renames)
			}
			if tt.wantCalls == 1 && h.client.renames[0] != testDeviceID+"="+strings.TrimSpace(tt.input) {
				t.Fatalf("rename call = %v", h.client.renames)
			}
			st := h.vm.State().Get()
			if st.Device.Current.Name != tt.wantName || h.profiles.profiles[testServerID].DeviceName != tt.wantName {
				t.Fatalf("name in state %q profile %q, want %q", st.Device.Current.Name, h.profiles.profiles[testServerID].DeviceName, tt.wantName)
			}
			if tt.wantField != "" && (st.Problem == nil || st.Problem.Field != tt.wantField) {
				t.Fatalf("Problem = %+v", st.Problem)
			}
			if tt.wantCalls == 1 && tt.renameErr == nil {
				for _, row := range st.Devices.Rows {
					if row.This && row.Device.Name != tt.wantName {
						t.Fatalf("row not updated: %+v", row)
					}
				}
			}
		})
	}
}

func TestRefreshMergesSections(t *testing.T) {
	h := newHarness(t)
	h.client.info = adminv1.ServerInfo{ServerVersion: "0.9.0", State: "ready"}
	h.client.telegram = adminv1.TelegramStatus{State: adminv1.TelegramLinked, Account: &adminv1.TelegramAccount{FirstName: "Ada"}}
	h.vm.Refresh(context.Background())
	st := h.vm.State().Get()
	if st.Loading || st.Problem != nil {
		t.Fatalf("state = %+v", st)
	}
	if st.Connection.ServerVersion != "0.9.0" || st.About.ServerVersion != "0.9.0" || st.About.AppVersion != "0.1.0" {
		t.Fatalf("versions: %+v / %+v", st.Connection, st.About)
	}
	if !st.Telegram.Linked() || !st.Device.Found || st.Device.Current.DeviceID != testDeviceID || len(st.Devices.Rows) != 2 {
		t.Fatalf("sections: %+v", st)
	}
	if !st.Devices.Rows[0].This || st.Devices.Rows[1].This {
		t.Fatalf("this-device marks: %+v", st.Devices.Rows)
	}
	if st.Connection.FingerprintDisplay == "" || !st.Connection.CanMoveToKeychain {
		t.Fatalf("connection: %+v", st.Connection)
	}
}

func TestRefreshPartialFailure(t *testing.T) {
	h := newHarness(t)
	h.client.statusErr = apiError(adminv1.CodeTelegramUnavailable)
	h.vm.Refresh(context.Background())
	st := h.vm.State().Get()
	if st.Problem == nil || st.Problem.Code != adminv1.CodeTelegramUnavailable || !st.Devices.Loaded || st.Telegram.Loaded {
		t.Fatalf("state = %+v", st)
	}
}

func TestOwnDeviceFallsBackToProfileID(t *testing.T) {
	h := newHarness(t)
	h.client.devices = []adminv1.Device{{DeviceID: testDeviceID, Name: "Office laptop"}, {DeviceID: "x"}}
	h.vm.RefreshDevices(context.Background())
	st := h.vm.State().Get()
	if !st.Device.Found || st.Device.Current.DeviceID != testDeviceID || !st.Devices.Rows[0].This {
		t.Fatalf("state = %+v", st.Devices)
	}
}

func TestRevokedRowsMarked(t *testing.T) {
	h := newHarness(t)
	at := testNow.Add(-time.Hour)
	h.client.devices = append(h.client.devices, adminv1.Device{DeviceID: "old", RevokedAt: &at})
	h.vm.RefreshDevices(context.Background())
	rows := h.vm.State().Get().Devices.Rows
	if len(rows) != 3 || !rows[2].Revoked || rows[0].Revoked {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRenameServer(t *testing.T) {
	tests := []struct {
		name      string
		label     string
		putErr    error
		want      string
		wantField string
	}{
		{"label saved to profile", " Lab ", nil, "Lab", ""},
		{"blank refused", "", nil, "home server", "name"},
		{"store failure refused", "Lab", errBoom, "home server", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.profiles.putErr = tt.putErr
			h.vm.RenameServer(tt.label)
			st := h.vm.State().Get()
			if st.Connection.Profile.Label != tt.want || h.vm.snapshot().Profile.Label != tt.want {
				t.Fatalf("label = %q", st.Connection.Profile.Label)
			}
			if tt.wantField != "" && (st.Problem == nil || st.Problem.Field != tt.wantField) {
				t.Fatalf("Problem = %+v", st.Problem)
			}
			if tt.putErr != nil && (st.Problem == nil || st.Problem.Presentation != model.PresentDialog) {
				t.Fatalf("Problem = %+v", st.Problem)
			}
		})
	}
}

func TestReverify(t *testing.T) {
	pin := "SHA256:" + fingerprintHex()
	other := "SHA256:" + strings.Repeat("ab", 32)
	tests := []struct {
		name    string
		mutate  func(h *harness)
		info    adminv1.CertificateInfo
		err     error
		want    ReverifyStatus
		problem bool
	}{
		{"pinned match", func(h *harness) {}, adminv1.CertificateInfo{SPKISHA256: pin}, nil, ReverifyMatch, false},
		{"pinned changed", func(h *harness) {}, adminv1.CertificateInfo{SPKISHA256: other}, nil, ReverifyChanged, false},
		{"system trust verifies", func(h *harness) {
			h.profile.Trust = adminv1.Trust{Mode: adminv1.TrustSystem}
		}, adminv1.CertificateInfo{VerifiesWithSystemRoots: true}, nil, ReverifyMatch, false},
		{"system trust fails", func(h *harness) {
			h.profile.Trust = adminv1.Trust{Mode: adminv1.TrustSystem}
		}, adminv1.CertificateInfo{SPKISHA256: other}, nil, ReverifyChanged, false},
		{"plain http not applicable", func(h *harness) { h.profile.BaseURL = "http://127.0.0.1:9443" }, adminv1.CertificateInfo{}, nil, ReverifyNotApplicable, false},
		{"probe failure", func(h *harness) {}, adminv1.CertificateInfo{}, &net.OpError{Op: "dial", Err: errBoom}, ReverifyNone, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.mutate(h)
			h.prober.info, h.prober.err = tt.info, tt.err
			v := New(h.client, func() time.Time { return testNow }, Session{Profile: h.profile, Tokens: h.tokens, Profiles: h.profiles, Prober: h.prober})
			before := h.profiles.profiles[testServerID]
			got := v.Reverify(context.Background())
			if got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
			if (v.State().Get().Problem != nil) != tt.problem {
				t.Fatalf("Problem = %+v", v.State().Get().Problem)
			}
			if h.profiles.profiles[testServerID] != before {
				t.Fatalf("re-verify changed the profile")
			}
			if tt.want == ReverifyChanged && tt.name == "pinned changed" {
				r := v.State().Get().Connection.Reverify
				if r.Expected == r.Got || r.Expected == "" {
					t.Fatalf("reverify detail = %+v", r)
				}
			}
		})
	}
}

func TestMoveToKeychain(t *testing.T) {
	tests := []struct {
		name        string
		keychainErr error
		profileErr  error
		wantMoved   bool
		wantStorage model.TokenStorage
		wantInOld   bool
		wantInNew   bool
	}{
		{"moves token then forgets the old copy", nil, nil, true, model.TokenKeychain, false, true},
		{"keychain failure keeps everything", model.ErrKeychainUnavailable, nil, false, model.TokenFile, true, false},
		{"profile failure rolls the keychain copy back", nil, errBoom, false, model.TokenFile, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.keychain.setErr = tt.keychainErr
			h.profiles.putErr = tt.profileErr
			moved := h.vm.MoveToKeychain()
			if moved != tt.wantMoved {
				t.Fatalf("moved = %v, problem %+v", moved, h.vm.State().Get().Problem)
			}
			_, oldErr := h.tokens.Get(testServerID)
			_, newErr := h.keychain.Get(testServerID)
			if (oldErr == nil) != tt.wantInOld || (newErr == nil) != tt.wantInNew {
				t.Fatalf("old err %v new err %v", oldErr, newErr)
			}
			st := h.vm.State().Get()
			if st.Connection.TokenStorage != tt.wantStorage || st.Connection.CanMoveToKeychain == tt.wantMoved {
				t.Fatalf("connection = %+v", st.Connection)
			}
		})
	}
}

func TestMoveToKeychainNotApplicableWithoutKeychain(t *testing.T) {
	h := newHarness(t)
	v := New(h.client, func() time.Time { return testNow }, Session{Profile: h.profile, Tokens: h.tokens, Profiles: h.profiles})
	if v.MoveToKeychain() || v.State().Get().Connection.CanMoveToKeychain {
		t.Fatalf("move must not be offered without a keychain")
	}
}

func TestPreferences(t *testing.T) {
	tests := []struct {
		name      string
		act       func(v *ViewModel) bool
		want      bool
		check     func(t *testing.T, h *harness, st PreferencesState)
		wantField string
	}{
		{"defaults read", func(v *ViewModel) bool { return true }, true, func(t *testing.T, h *harness, st PreferencesState) {
			if st.Theme != ThemeSystem || st.UploadConcurrency != 2 || st.ClipboardClearSeconds != 60 || st.DownloadDir != "/home/me/Downloads" {
				t.Fatalf("defaults = %+v", st)
			}
		}, ""},
		{"theme dark saved", func(v *ViewModel) bool { return v.SetTheme(ThemeDark) }, true, func(t *testing.T, h *harness, st PreferencesState) {
			if st.Theme != ThemeDark || h.prefs.strings[PrefTheme] != ThemeDark {
				t.Fatalf("theme = %+v", st)
			}
		}, ""},
		{"unknown theme refused", func(v *ViewModel) bool { return v.SetTheme("neon") }, false, nil, "theme"},
		{"concurrency 4 saved", func(v *ViewModel) bool { return v.SetUploadConcurrency(4) }, true, func(t *testing.T, h *harness, st PreferencesState) {
			if st.UploadConcurrency != 4 || h.prefs.ints[PrefUploadConcurrency] != 4 {
				t.Fatalf("concurrency = %+v", st)
			}
		}, ""},
		{"concurrency 0 refused", func(v *ViewModel) bool { return v.SetUploadConcurrency(0) }, false, nil, "upload_concurrency"},
		{"concurrency 5 refused", func(v *ViewModel) bool { return v.SetUploadConcurrency(5) }, false, nil, "upload_concurrency"},
		{"clipboard zero disables clearing", func(v *ViewModel) bool { return v.SetClipboardClearSeconds(0) }, true, func(t *testing.T, h *harness, st PreferencesState) {
			if st.ClipboardClearSeconds != 0 {
				t.Fatalf("clipboard = %+v", st)
			}
		}, ""},
		{"clipboard negative refused", func(v *ViewModel) bool { return v.SetClipboardClearSeconds(-1) }, false, nil, "clipboard_clear_seconds"},
		{"download dir saved", func(v *ViewModel) bool { return v.SetDownloadDir(" /data/dl ") }, true, func(t *testing.T, h *harness, st PreferencesState) {
			if st.DownloadDir != "/data/dl" {
				t.Fatalf("dir = %+v", st)
			}
		}, ""},
		{"blank download dir refused", func(v *ViewModel) bool { return v.SetDownloadDir(" ") }, false, nil, "download_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if got := tt.act(h.vm); got != tt.want {
				t.Fatalf("result = %v", got)
			}
			st := h.vm.State().Get()
			if tt.check != nil {
				tt.check(t, h, st.Preferences)
			}
			if tt.wantField != "" && (st.Problem == nil || st.Problem.Field != tt.wantField || st.Problem.Presentation != model.PresentInline) {
				t.Fatalf("Problem = %+v", st.Problem)
			}
		})
	}
}

func TestPreferencesClampStoredValues(t *testing.T) {
	h := newHarness(t)
	h.prefs.strings[PrefTheme] = "garbage"
	h.prefs.ints[PrefUploadConcurrency] = 99
	h.prefs.ints[PrefClipboardClearSeconds] = -5
	h.vm.Refresh(context.Background())
	st := h.vm.State().Get().Preferences
	if st.Theme != ThemeSystem || st.UploadConcurrency != MaxUploadConcurrency || st.ClipboardClearSeconds != 0 {
		t.Fatalf("preferences = %+v", st)
	}
}

func TestPreferencesWithoutStore(t *testing.T) {
	v := New(&fakeClient{}, func() time.Time { return testNow }, Session{})
	if v.SetTheme(ThemeDark) {
		t.Fatalf("SetTheme succeeded without a store")
	}
	if p := v.State().Get().Problem; p == nil || p.Presentation != model.PresentDialog {
		t.Fatalf("Problem = %+v", p)
	}
}

func TestLastRouteKey(t *testing.T) {
	if got := LastRouteKey("abc"); got != "last_route.abc" {
		t.Fatalf("LastRouteKey = %q", got)
	}
}

func TestDisplayFingerprint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"canonical pin", "SHA256:" + strings.Repeat("ab12", 16), strings.TrimSuffix(strings.Repeat("ab12 ab12 ab12 ab12\n", 4), "\n")},
		{"no prefix", strings.Repeat("ab12", 5), "ab12 ab12 ab12 ab12\nab12"},
		{"empty", "", ""},
		{"short tail group", "abcdef", "abcd ef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayFingerprint(tt.in); got != tt.want {
				t.Fatalf("DisplayFingerprint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAboutLists(t *testing.T) {
	h := newHarness(t)
	about := h.vm.State().Get().About
	if about.AppVersion != "0.1.0" || about.GoVersion == "" || about.FyneVersion == "" || len(about.Licences) != 3 {
		t.Fatalf("about = %+v", about)
	}
	names := about.Licences[0].Name + about.Licences[1].Name + about.Licences[2].Name
	for _, want := range []string{"Fyne", "go-keyring", "golang.org/x/image"} {
		if !strings.Contains(names, want) {
			t.Fatalf("licences lack %q: %+v", want, about.Licences)
		}
	}
}

func TestTelegramRefreshAndLinked(t *testing.T) {
	h := newHarness(t)
	h.client.telegram = adminv1.TelegramStatus{State: adminv1.TelegramLinked, Account: &adminv1.TelegramAccount{FirstName: "Ada"}}
	h.vm.RefreshTelegram(context.Background())
	if !h.vm.State().Get().Telegram.Linked() {
		t.Fatalf("not linked")
	}
	h.client.statusErr = apiError(adminv1.CodeTelegramUnavailable)
	h.vm.RefreshTelegram(context.Background())
	st := h.vm.State().Get()
	if st.Problem == nil || !st.Telegram.Linked() {
		t.Fatalf("failed refresh must keep prior data and report: %+v", st)
	}
}
