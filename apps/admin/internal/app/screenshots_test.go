package app

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	bucketsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
	connectvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
	dashboardvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
	keysvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
	telegramvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

const shotsDirVariable = "BUTTERDB_ADMIN_SHOTS_DIR"

var shotWindowSize = fyne.NewSize(1280, 800)

type shooter struct {
	h     *harness
	dir   string
	count int
}

func relayout(o fyne.CanvasObject) {
	switch c := o.(type) {
	case *fyne.Container:
		if c.Layout != nil {
			c.Layout.Layout(c.Objects, c.Size())
		}
		for _, child := range c.Objects {
			relayout(child)
		}
	case fyne.Widget:
		r := test.WidgetRenderer(c)
		r.Layout(c.Size())
		for _, child := range r.Objects() {
			relayout(child)
		}
	}
}

func (s *shooter) relayoutAll() {
	canvas := s.h.win.Canvas()
	for i := 0; i < 3; i++ {
		relayout(canvas.Content())
		for _, overlay := range canvas.Overlays().List() {
			relayout(overlay)
		}
	}
}

func (s *shooter) shot(name string) {
	s.h.t.Helper()
	s.h.settle()
	s.relayoutAll()
	s.count++
	path := filepath.Join(s.dir, fmt.Sprintf("%02d-%s.png", s.count, name))
	file, err := os.Create(path)
	if err != nil {
		s.h.t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	if err := png.Encode(file, s.h.win.Canvas().Capture()); err != nil {
		s.h.t.Fatalf("encode %s: %v", path, err)
	}
}

func TestCaptureScreenshots(t *testing.T) {
	root := os.Getenv(shotsDirVariable)
	if root == "" {
		t.Skip("set " + shotsDirVariable + " to write the redesign screenshots")
	}
	for _, mode := range []string{settingsvm.ThemeLight, settingsvm.ThemeDark} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(root, mode)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			captureTour(t, dir, mode)
		})
	}
}

func captureTour(t *testing.T, dir, mode string) {
	admin := newFakeAdmin(t, fixedNow)
	h := newHarness(t)
	h.app.Preferences().SetString(settingsvm.PrefTheme, mode)
	h.start()
	h.win.Resize(shotWindowSize)
	s := &shooter{h: h, dir: dir}

	h.waitRoute(model.RouteConnect)
	s.shot("connect-empty")
	test.Type(h.waitEntry("host or host:port"), admin.host())
	test.Tap(h.waitButton("Continue"))
	h.waitLabel("Verify this server")
	s.shot("connect-certificate")
	test.Tap(h.waitButton("Fingerprints match"))
	test.Type(h.waitEntry("bdst1_…"), testSetupToken)
	s.shot("connect-claim")
	test.Tap(h.waitButton("Claim"))

	captureTelegram(s)
	captureDashboard(s)
	captureBuckets(s)
	captureKeys(s)
	captureObjects(t, s)
	captureSettings(s)
	captureProblems(s)
	captureConnectStates(s)
}

func captureTelegram(s *shooter) {
	h := s.h
	h.waitRoute(model.RouteTelegram)
	h.waitEntry("digits only")
	s.shot("telegram-app")
	test.Type(h.waitEntry("digits only"), "123456")
	test.Type(h.waitEntry("32 hexadecimal characters"), testAPIHash)
	test.Tap(h.waitButton("Save app credentials"))
	test.Type(h.waitEntry("+447700900134"), "+447700900134")
	s.shot("telegram-phone")
	test.Tap(h.waitButton("Send code"))
	h.waitButton("Submit code")
	h.waitFor("code step", func() bool { return h.session().models.telegram.State().Get().Step == telegramvm.StepCode })
	s.shot("telegram-code")
	tg := h.session().models.telegram
	saved := tg.State().Get()
	password := saved
	password.Step = telegramvm.StepPassword
	if saved.Login != nil {
		login := *saved.Login
		login.PasswordHint = "the usual one"
		login.PasswordAttemptsLeft = 3
		password.Login = &login
	}
	tg.State().Set(password)
	s.shot("telegram-password")
	tg.State().Set(saved)
	h.settle()
	test.Type(h.onlyVisibleEntry(), testLoginCode)
	test.Tap(h.waitButton("Submit code"))
	h.waitButton("Continue to dashboard")
	s.shot("telegram-done")
	test.Tap(h.waitButton("Continue to dashboard"))
	h.waitRoute(model.RouteDashboard)
}

func richStatus() adminv1.Status {
	until := fixedNow.Add(90 * time.Second)
	return adminv1.Status{
		ServerID:      testServerID,
		ServerVersion: "0.1.0",
		Commit:        "b73a0ec",
		StartedAt:     fixedNow.Add(-25 * time.Hour),
		UptimeSeconds: 90061,
		State:         "ready",
		Telegram: adminv1.TelegramHealth{
			Linked:            true,
			Connected:         true,
			Account:           &adminv1.TelegramAccount{FirstName: "Butter", Username: "butterowner"},
			FloodWaitUntil:    &until,
			SendRatePerMinute: 20,
		},
		Components: []adminv1.Component{
			{Name: "telegram", Status: "ok", Since: fixedNow.Add(-2 * time.Hour)},
			{Name: "projection", Status: "ok", Since: fixedNow.Add(-25 * time.Hour)},
			{Name: "sync", Status: "degraded", Detail: "catching up", Since: fixedNow.Add(-5 * time.Minute)},
		},
		Endpoints: adminv1.Endpoints{S3PublicURL: testS3Endpoint, S3Region: "us-east-1"},
		Replica:   adminv1.ReplicaSummary{State: "not_available"},
	}
}

func richUsage(cacheLimit int64) adminv1.Usage {
	return adminv1.Usage{
		Buckets:          4,
		Objects:          18234,
		StoredBytes:      41 << 30,
		PlaintextBytes:   40 << 30,
		EncryptedObjects: 1207,
		BlockCache:       adminv1.CacheUsage{UsedBytes: cacheLimit / 3, LimitBytes: cacheLimit, HitRatio1h: 0.93},
		Spool:            adminv1.SpoolUsage{UsedBytes: 220 << 20, LimitBytes: 4 << 30, JobsInFlight: 2},
		ComputedAt:       fixedNow,
	}
}

func captureDashboard(s *shooter) {
	h := s.h
	dash := h.session().models.dashboard
	h.waitFor("dashboard loaded", func() bool { return dash.State().Get().HasStatus })
	dash.State().Set(dashboardvm.State{HasStatus: true, HasUsage: true, Status: richStatus(), Usage: richUsage(0), RefreshedAt: fixedNow})
	s.shot("dashboard-cache-off")
	dash.State().Set(dashboardvm.State{HasStatus: true, HasUsage: true, Status: richStatus(), Usage: richUsage(2 << 30), RefreshedAt: fixedNow})
	s.shot("dashboard-cache-on")
}

func sampleBuckets() []adminv1.Bucket {
	return []adminv1.Bucket{
		{Name: "avatars", Policy: "private", EncryptionDefault: "none", CacheMaxAgeSeconds: 3600, ObjectCount: 912, StoredBytes: 88 << 20, CreatedAt: fixedNow.Add(-40 * 24 * time.Hour)},
		{Name: "backups", Policy: "private", EncryptionDefault: "tde1", CacheMaxAgeSeconds: 0, ObjectCount: 1207, StoredBytes: 31 << 30, CreatedAt: fixedNow.Add(-20 * 24 * time.Hour)},
		{Name: "photos", Policy: "public-read", EncryptionDefault: "none", CacheMaxAgeSeconds: 86400, ObjectCount: 16100, StoredBytes: 9 << 30, CreatedAt: fixedNow.Add(-3 * time.Hour)},
		{Name: "site-assets", Policy: "public-read", EncryptionDefault: "none", CacheMaxAgeSeconds: 604800, ObjectCount: 15, StoredBytes: 2 << 20, CreatedAt: fixedNow.Add(-50 * time.Minute)},
	}
}

func captureBuckets(s *shooter) {
	h := s.h
	h.createBucketThroughUI("photos")
	models := h.session().models
	models.buckets.State().Set(bucketsvm.State{Buckets: sampleBuckets()})
	h.settle()
	s.shot("buckets-list")
	s.hover(h.waitButton("New bucket").(fyne.CanvasObject))
	s.shot("buckets-hover-primary")
	test.MoveMouse(h.win.Canvas(), fyne.NewPos(1, 1))
	test.Tap(h.waitButton("New bucket"))
	test.Type(h.waitOverlayEntry("my-bucket"), "invoices")
	s.shot("buckets-create")
	test.Tap(h.waitOverlayButton("Cancel"))
	h.waitNoOverlay()
}

func captureKeys(s *shooter) {
	h := s.h
	h.typeShortcut(fyne.Key3)
	h.waitRoute(model.RouteKeys)
	test.Tap(h.waitButton("New key"))
	test.Type(h.waitOverlayEntry("website prod"), "website prod")
	s.shot("keys-create")
	test.Tap(h.waitOverlayCheck("All buckets, including future ones"))
	test.Tap(h.waitOverlayButton("Create key"))
	h.waitOverlayCheck("I have stored the secret")
	s.shot("keys-secret")
	test.Tap(h.waitOverlayCheck("I have stored the secret"))
	test.Tap(h.waitOverlayButton("Done"))
	h.waitNoOverlay()
	keys := h.session().models.keys
	st := keys.State().Get()
	used := fixedNow.Add(-3 * time.Minute)
	st.Keys = append(st.Keys,
		adminv1.AccessKey{AccessKeyID: "BDAKQ7M2X9VWT4RZ8P3L", Name: "backup job", Status: "active", Scope: adminv1.Scope{Buckets: []string{"backups"}, Actions: []string{"read", "list", "write"}}, CreatedAt: fixedNow.Add(-72 * time.Hour), LastUsedAt: &used},
		adminv1.AccessKey{AccessKeyID: "BDAKH5N8C2KD6YQW1E7T", Name: "cdn reader", Status: "active", Scope: adminv1.Scope{Buckets: []string{"photos", "site-assets"}, Actions: []string{"read", "list"}}, CreatedAt: fixedNow.Add(-30 * 24 * time.Hour)},
	)
	keys.State().Set(keysvm.State{Keys: st.Keys, BucketNames: st.BucketNames})
	h.settle()
	s.shot("keys-list")
}

func captureObjects(t *testing.T, s *shooter) {
	h := s.h
	h.browseBucket("photos")
	h.uploadFile("photos", "holiday.png", noisePNG(t, 256))
	h.uploadFile("photos", "notes.txt", []byte("butterDB keeps the bytes in Telegram."))
	h.uploadFile("photos", "report.csv", []byte("bucket,objects\nphotos,3\n"))
	h.refreshObjects()
	models := h.session().models
	models.objects.Focus(h.session().ctx, "holiday.png")
	h.settle()
	time.Sleep(50 * time.Millisecond)
	s.shot("objects-browser")
	h.win.Resize(fyne.NewSize(1024, 680))
	s.shot("objects-minimum-window")
	h.win.Resize(shotWindowSize)
	models.objects.ToggleObject("holiday.png")
	h.settle()
	test.Tap(h.waitButton("Copy presigned URL…"))
	h.settle()
	test.Tap(h.waitOverlayButton("Generate"))
	h.waitFor("presigned URL", func() bool { return h.overlayHasLabel("Expires at") })
	s.shot("objects-presign")
	s.closeOverlays()
	models.objects.ToggleObject("holiday.png")
	models.objects.SelectAll()
	h.settle()
	test.Tap(h.waitButton("Delete…"))
	s.shot("objects-delete-confirm")
	test.Tap(h.waitOverlayButton("Cancel"))
	h.waitNoOverlay()
}

func (h *harness) overlayHasLabel(substr string) bool {
	for _, o := range h.overlayObjects() {
		if l, ok := o.(*widget.Label); ok && strings.Contains(l.Text, substr) {
			return true
		}
	}
	return false
}

func (s *shooter) hover(o fyne.CanvasObject) {
	pos := s.h.app.Driver().AbsolutePositionForObject(o)
	test.MoveMouse(s.h.win.Canvas(), pos.AddXY(o.Size().Width/2, o.Size().Height/2))
}

func (s *shooter) closeOverlays() {
	canvas := s.h.win.Canvas()
	for top := canvas.Overlays().Top(); top != nil; top = canvas.Overlays().Top() {
		canvas.Overlays().Remove(top)
	}
}

func sampleDevices() []settingsvm.DeviceRow {
	revoked := fixedNow.Add(-time.Hour)
	return []settingsvm.DeviceRow{
		{Device: adminv1.Device{DeviceID: testDeviceID, Name: "Test laptop", Platform: "linux/amd64", CreatedVia: "setup_claim", LastSeenAt: fixedNow, LastSeenIP: "203.0.113.7"}, This: true},
		{Device: adminv1.Device{DeviceID: "d2", Name: "Studio desktop", Platform: "darwin/arm64", CreatedVia: "pairing", LastSeenAt: fixedNow.Add(-26 * time.Hour), LastSeenIP: "198.51.100.23"}},
		{Device: adminv1.Device{DeviceID: "d3", Name: "Old phone", Platform: "windows/amd64", CreatedVia: "host_command", LastSeenAt: fixedNow.Add(-40 * 24 * time.Hour), LastSeenIP: "192.0.2.44", RevokedAt: &revoked}, Revoked: true},
	}
}

func sampleAudit() []adminv1.AuditEvent {
	device := adminv1.AuditActor{Type: "device", DeviceID: testDeviceID, DeviceName: "Test laptop"}
	return []adminv1.AuditEvent{
		{ID: 5, At: fixedNow.Add(-2 * time.Minute), Actor: device, Action: "bucket.create", Target: "photos", Outcome: "ok", RemoteAddr: "203.0.113.7"},
		{ID: 4, At: fixedNow.Add(-6 * time.Minute), Actor: device, Action: "access_key.create", Target: "website prod", Outcome: "ok", RemoteAddr: "203.0.113.7"},
		{ID: 3, At: fixedNow.Add(-9 * time.Minute), Actor: adminv1.AuditActor{Type: "system"}, Action: "telegram.link", Target: "Butter", Outcome: "ok"},
		{ID: 2, At: fixedNow.Add(-11 * time.Minute), Actor: adminv1.AuditActor{Type: "anonymous"}, Action: "setup.claim", Outcome: "error", ErrorCode: "setup_token_invalid", RemoteAddr: "198.51.100.9"},
	}
}

func captureSettings(s *shooter) {
	h := s.h
	h.typeShortcut(fyne.Key6)
	h.waitRoute(model.RouteSettings)
	settings := h.session().models.settings
	for _, step := range []string{"connection", "device", "devices", "telegram", "activity", "about", "preferences"} {
		h.comp.sh.Navigator().Navigate(model.RouteSettings, model.Params{model.ParamStep: step})
		h.settle()
		s.closeOverlays()
		st := settings.State().Get()
		st.Problem = nil
		st.Devices = settingsvm.DevicesState{Loaded: true, Rows: sampleDevices()}
		st.Activity = settingsvm.ActivityState{Loaded: true, Events: sampleAudit(), NextCursor: "next"}
		settings.State().Set(st)
		h.comp.sh.Notifier().ClearBanner()
		s.shot("settings-" + step)
	}
}

func captureProblems(s *shooter) {
	h := s.h
	s.closeOverlays()
	h.typeShortcut(fyne.Key2)
	h.waitRoute(model.RouteBuckets)
	h.comp.sh.Notifier().Problem(model.Problem{
		Code: adminv1.CodeTelegramFloodWait, Title: "Telegram asked the server to wait until 09:32:00.",
		Presentation: model.PresentBanner, Retryable: true, RetryAt: fixedNow.Add(90 * time.Second), RequestID: "req-7f3a",
	})
	s.shot("banner-problem")
	h.comp.sh.Notifier().ClearBanner()
	h.comp.sh.Notifier().Problem(model.Problem{
		Code: adminv1.CodeInternal, Title: "The server hit an error. Check its log for request req-91c2.",
		Presentation: model.PresentDialog, RequestID: "req-91c2", Details: map[string]any{"http_status": 500},
	})
	s.shot("dialog-problem")
	s.closeOverlays()
}

func captureConnectStates(s *shooter) {
	h := s.h
	connect := h.comp.connectVM
	h.comp.sh.Navigator().Navigate(model.RouteConnect, nil)
	h.settle()
	s.closeOverlays()
	s.shot("connect-profiles")
	base := connect.State().Get()
	pair := base
	pair.Step = connectvm.StepPair
	pair.SavedMessagesEnabled = true
	pair.PairingRequested = true
	pair.PairingExpiresAt = time.Now().Add(9*time.Minute + 41*time.Second)
	pair.PairingCode = "K7Q2M-9XW4T"
	pair.PairingCodeComplete = true
	connect.State().Set(pair)
	s.shot("connect-pairing")
	storage := base
	storage.Step = connectvm.StepStorageChoice
	connect.State().Set(storage)
	s.shot("connect-storage")
	mismatch := base
	mismatch.Step = connectvm.StepPinMismatch
	mismatch.Mismatch = &connectvm.MismatchView{
		ServerLabel: "home server",
		Expected:    connectvm.FingerprintLines("4f1c9a0e7b2d3c4e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a"),
		Received:    connectvm.FingerprintLines("9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f30211203f4e5d6c7b8a9"),
	}
	connect.State().Set(mismatch)
	s.shot("connect-pin-mismatch")
	connect.State().Set(base)
}
