package vm

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func TestRefreshMergesSections(t *testing.T) {
	h := newHarness(t)
	h.client.status = adminv1.Status{ServerVersion: "0.9.0"}
	h.client.telegram = adminv1.TelegramStatus{State: adminv1.TelegramLinked, Account: &adminv1.TelegramAccount{FirstName: "Ada"}}
	h.vm.Refresh(context.Background())
	st := h.vm.State().Get()
	if st.Loading || st.Problem != nil {
		t.Fatalf("state = %+v", st)
	}
	if st.About.ServerVersion != "0.9.0" || st.About.AppVersion != "0.1.0" {
		t.Fatalf("versions: %+v", st.About)
	}
	if !st.Telegram.Linked() || !st.Project.Open || st.Project.Project.Name != "photos" {
		t.Fatalf("sections: %+v", st)
	}
}

func TestRefreshPartialFailure(t *testing.T) {
	h := newHarness(t)
	h.client.telegramErr = apiError(adminv1.CodeTelegramUnavailable)
	h.vm.Refresh(context.Background())
	st := h.vm.State().Get()
	if st.Problem == nil || st.Problem.Code != adminv1.CodeTelegramUnavailable || st.Telegram.Loaded || !st.Project.Open {
		t.Fatalf("state = %+v", st)
	}
}

func TestAboutLists(t *testing.T) {
	h := newHarness(t)
	about := h.vm.State().Get().About
	if about.AppVersion != "0.1.0" || about.GoVersion == "" || about.FyneVersion == "" || len(about.Licences) != 2 {
		t.Fatalf("about = %+v", about)
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

func TestTelegramRefreshAndLinked(t *testing.T) {
	h := newHarness(t)
	h.client.telegram = adminv1.TelegramStatus{State: adminv1.TelegramLinked, Account: &adminv1.TelegramAccount{FirstName: "Ada"}}
	h.vm.RefreshTelegram(context.Background())
	if !h.vm.State().Get().Telegram.Linked() {
		t.Fatalf("not linked")
	}
	h.client.telegramErr = apiError(adminv1.CodeTelegramUnavailable)
	h.vm.RefreshTelegram(context.Background())
	st := h.vm.State().Get()
	if st.Problem == nil || !st.Telegram.Linked() {
		t.Fatalf("failed refresh must keep prior data and report: %+v", st)
	}
}
