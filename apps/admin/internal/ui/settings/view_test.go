package settings

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClient struct {
	mu          sync.Mutex
	revokeErrs  []error
	revokeCalls []bool
	unlinks     int
	audits      []adminv1.AuditQuery
}

func (f *fakeClient) ServerInfo(context.Context) (adminv1.ServerInfo, error) {
	return adminv1.ServerInfo{ServerVersion: "0.9.0", State: "claimed"}, nil
}

func (f *fakeClient) ListDevices(context.Context) (adminv1.DeviceList, error) {
	revoked := testNow.Add(-time.Hour)
	return adminv1.DeviceList{Devices: []adminv1.Device{
		{DeviceID: "self", Name: "Office laptop", Platform: "linux/amd64", CreatedVia: "setup", Current: true, LastSeenAt: testNow.Add(-2 * time.Minute), LastSeenIP: "203.0.113.7"},
		{DeviceID: "old", Name: "Old phone", RevokedAt: &revoked},
	}}, nil
}

func (f *fakeClient) RenameDevice(_ context.Context, id string, req adminv1.UpdateDeviceRequest) (adminv1.Device, error) {
	return adminv1.Device{DeviceID: id, Name: req.Name, Current: true}, nil
}

func (f *fakeClient) RevokeDevice(_ context.Context, _ string, confirmLast bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeCalls = append(f.revokeCalls, confirmLast)
	if len(f.revokeErrs) == 0 {
		return nil
	}
	err := f.revokeErrs[0]
	f.revokeErrs = f.revokeErrs[1:]
	return err
}

func (f *fakeClient) RotateDeviceToken(context.Context) (adminv1.DeviceCredentials, error) {
	return adminv1.DeviceCredentials{DeviceToken: "new"}, nil
}

func (f *fakeClient) TelegramStatus(context.Context) (adminv1.TelegramStatus, error) {
	return adminv1.TelegramStatus{
		State:   adminv1.TelegramLinked,
		Account: &adminv1.TelegramAccount{FirstName: "Ada", Username: "ada", PhoneMasked: "+49 *** 12", Connected: true, LinkedAt: testNow.Add(-72 * time.Hour)},
	}, nil
}

func (f *fakeClient) UnlinkTelegram(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlinks++
	return adminv1.TelegramStatus{State: adminv1.TelegramReady}, nil
}

func (f *fakeClient) Audit(_ context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, q)
	return adminv1.AuditPage{
		Events:     []adminv1.AuditEvent{{ID: 1, At: testNow, Actor: adminv1.AuditActor{Type: "device", DeviceName: "Office laptop"}, Action: "bucket.create", Target: "photos", Outcome: "success", RemoteAddr: "203.0.113.7"}},
		NextCursor: "next",
	}, nil
}

type fakeNav struct {
	mu        sync.Mutex
	navigated []model.Route
	disabled  map[model.Route]string
}

func (n *fakeNav) Navigate(r model.Route, _ model.Params) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.navigated = append(n.navigated, r)
}

func (n *fakeNav) Current() model.Route { return model.RouteSettings }

func (n *fakeNav) SetEnabled(r model.Route, enabled bool, reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.disabled == nil {
		n.disabled = map[model.Route]string{}
	}
	if !enabled {
		n.disabled[r] = reason
	}
}

func (n *fakeNav) routes() []model.Route {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]model.Route(nil), n.navigated...)
}

func (n *fakeNav) reasonFor(r model.Route) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.disabled[r]
}

type fakeNotifier struct {
	problems []model.Problem
	infos    []string
	cleared  int
}

func (n *fakeNotifier) Problem(p model.Problem) { n.problems = append(n.problems, p) }
func (n *fakeNotifier) Info(text string)        { n.infos = append(n.infos, text) }
func (n *fakeNotifier) ClearBanner()            { n.cleared++ }

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type memTokens struct{ tokens map[string]string }

func (m *memTokens) Get(id string) (string, error) {
	if t, ok := m.tokens[id]; ok {
		return t, nil
	}
	return "", model.ErrTokenNotFound
}
func (m *memTokens) Set(id, token string) error { m.tokens[id] = token; return nil }
func (m *memTokens) Delete(id string) error     { delete(m.tokens, id); return nil }
func (m *memTokens) Kind() model.TokenStorage   { return model.TokenFile }

type memProfiles struct{ profile model.Profile }

func (m *memProfiles) List() ([]model.Profile, error)    { return nil, nil }
func (m *memProfiles) Get(string) (model.Profile, error) { return m.profile, nil }
func (m *memProfiles) Put(p model.Profile) error         { m.profile = p; return nil }
func (m *memProfiles) Delete(string) error               { m.profile = model.Profile{}; return nil }
func (m *memProfiles) LastUsed() (string, error)         { return "", nil }
func (m *memProfiles) SetLastUsed(string) error          { return nil }

type memPrefs struct {
	strings map[string]string
	ints    map[string]int
}

func (m *memPrefs) StringWithFallback(k, d string) string {
	if v, ok := m.strings[k]; ok {
		return v
	}
	return d
}
func (m *memPrefs) SetString(k, v string) { m.strings[k] = v }
func (m *memPrefs) IntWithFallback(k string, d int) int {
	if v, ok := m.ints[k]; ok {
		return v
	}
	return d
}
func (m *memPrefs) SetInt(k string, v int) { m.ints[k] = v }

type rig struct {
	screen  *Screen
	vm      *vm.ViewModel
	client  *fakeClient
	nav     *fakeNav
	notif   *fakeNotifier
	prefs   *memPrefs
	profile *memProfiles
	tokens  *memTokens
	win     fyne.Window
	queue   *uiQueue
}

func newRig(t *testing.T, withNotifier bool) *rig {
	t.Helper()
	test.NewTempApp(t)
	profile := model.Profile{
		ServerID: "srv", Label: "home server", BaseURL: "https://203.0.113.10:9443",
		Trust:    adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: "SHA256:" + strings.Repeat("ab12", 16)},
		DeviceID: "self", DeviceName: "Office laptop", TokenStorage: model.TokenFile,
		TokenIssuedAt: testNow.Add(-48 * time.Hour),
	}
	r := &rig{
		client:  &fakeClient{},
		nav:     &fakeNav{},
		notif:   &fakeNotifier{},
		prefs:   &memPrefs{strings: map[string]string{}, ints: map[string]int{}},
		profile: &memProfiles{profile: profile},
		tokens:  &memTokens{tokens: map[string]string{"srv": "tok"}},
	}
	r.vm = vm.New(r.client, func() time.Time { return testNow }, vm.Session{
		Profile: profile, Tokens: r.tokens, Profiles: r.profile, Preferences: r.prefs, AppVersion: "0.1.0", DefaultDownloadDir: "/home/me/Downloads",
	})
	r.win = test.NewTempWindow(t, widget.NewLabel("placeholder"))
	var n *fakeNotifier
	if withNotifier {
		n = r.notif
	}
	if n != nil {
		r.screen = NewScreen(r.vm, r.nav, n, &fakeClipboard{}, r.win)
	} else {
		r.screen = NewScreen(r.vm, r.nav, nil, &fakeClipboard{}, r.win)
	}
	r.win.SetContent(r.screen.Content())
	return r
}

func walk(o fyne.CanvasObject, fn func(fyne.CanvasObject)) {
	if o == nil {
		return
	}
	fn(o)
	switch c := o.(type) {
	case *fyne.Container:
		for _, child := range c.Objects {
			walk(child, fn)
		}
	case *container.Scroll:
		walk(c.Content, fn)
	case *container.Split:
		walk(c.Leading, fn)
		walk(c.Trailing, fn)
	}
}

func visibleTexts(o fyne.CanvasObject) string {
	var out []string
	walk(o, func(co fyne.CanvasObject) {
		if !co.Visible() {
			return
		}
		switch w := co.(type) {
		case *widget.Label:
			out = append(out, w.Text)
		case *widget.Button:
			out = append(out, w.Text)
		}
	})
	return strings.Join(out, "\n")
}

func findButton(o fyne.CanvasObject, text string) *widget.Button {
	var found *widget.Button
	walk(o, func(co fyne.CanvasObject) {
		if b, ok := co.(*widget.Button); ok && b.Text == text && found == nil {
			found = b
		}
	})
	return found
}

type uiQueue struct{ funcs chan func() }

func newUIQueue() *uiQueue { return &uiQueue{funcs: make(chan func(), 64)} }

func (q *uiQueue) post(f func()) { q.funcs <- f }

func (q *uiQueue) drain() {
	for {
		select {
		case f := <-q.funcs:
			f()
		default:
			return
		}
	}
}

func (r *rig) queueUI() *uiQueue {
	q := newUIQueue()
	r.screen.view.post = q.post
	r.queue = q
	return q
}

func (r *rig) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.queue.drain()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestScreenIdentity(t *testing.T) {
	r := newRig(t, false)
	s := r.screen
	if s.Route() != model.RouteSettings || s.Shortcut() != fyne.Key6 || s.Title() != "Settings" || s.Icon() == nil {
		t.Fatalf("identity = %q %q %q", s.Route(), s.Shortcut(), s.Title())
	}
}

func TestRendersEverySection(t *testing.T) {
	r := newRig(t, false)
	r.vm.Refresh(context.Background())
	text := visibleTexts(r.screen.view.root)
	for _, step := range []string{stepDevices, stepTelegram, stepAbout, stepPreferences, stepConnection} {
		r.screen.view.showStep(step, true)
		text += "\n" + visibleTexts(r.screen.view.root)
	}
	for _, want := range []string{
		"https://203.0.113.10:9443", "0.9.0", "Pinned certificate key", "ab12 ab12 ab12 ab12", "Protected file",
		"Office laptop (This device)", "linux/amd64", "203.0.113.7", "revoked at 2026-11-16 08:30", "Revoke…",
		"Ada", "@ada", "+49 *** 12", "Unlink…", safetyNotice,
		"Fyne", "BSD-3-Clause", "go-keyring", "MIT", "0.1.0",
		"Theme", "Upload concurrency (1 to 4)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered text lacks %q", want)
		}
	}
	if strings.Contains(text, "Revoke…\nRevoke…\nRevoke…") {
		t.Errorf("revoked device must not offer revoke")
	}
}

func TestShowSelectsStepFromParams(t *testing.T) {
	tests := []struct {
		name   string
		params model.Params
		want   string
	}{
		{"f1 opens about", model.Params{model.ParamStep: "about"}, stepAbout},
		{"unknown step is ignored", model.Params{model.ParamStep: "nope"}, stepConnection},
		{"no step keeps the first section", nil, stepConnection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, false)
			r.screen.view.selectStep(tt.params[model.ParamStep])
			if got := r.screen.view.selectedStep(); got != tt.want {
				t.Fatalf("selected = %q", got)
			}
			for _, e := range r.screen.view.entries {
				if e.panel.object().Visible() != (e.step == tt.want) {
					t.Fatalf("panel %q visibility wrong", e.step)
				}
			}
		})
	}
}

func TestShowRefreshesInBackground(t *testing.T) {
	r := newRig(t, false)
	r.queueUI()
	loaded := make(chan struct{}, 4)
	cancel := r.vm.State().Subscribe(func(s vm.State) {
		if !s.Loading && s.Devices.Loaded && s.Telegram.Loaded {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	})
	defer cancel()
	r.screen.Show(context.Background(), model.Params{model.ParamStep: "devices"})
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatalf("Show did not load data")
	}
	r.queue.drain()
	if !strings.Contains(visibleTexts(r.screen.view.root), "Office laptop (This device)") {
		t.Fatalf("devices not rendered")
	}
}

func TestUnlinkButtonOnlyWhenLinked(t *testing.T) {
	r := newRig(t, false)
	button := findButton(r.screen.view.root, "Unlink…")
	if button == nil || !button.Disabled() {
		t.Fatalf("unlink must start disabled")
	}
	r.vm.RefreshTelegram(context.Background())
	if button.Disabled() {
		t.Fatalf("unlink must enable once linked")
	}
}

func TestUnlinkRequiresTypedWordInDialog(t *testing.T) {
	r := newRig(t, false)
	r.vm.RefreshTelegram(context.Background())
	r.queueUI()
	test.Tap(findButton(r.screen.view.root, "Unlink…"))
	focused, ok := r.win.Canvas().Focused().(fyne.Focusable)
	if !ok {
		t.Fatalf("typed confirmation entry is not focused")
	}
	confirm := overlayConfirm(r.win)
	if confirm == nil || confirm.Disabled() == false {
		t.Fatalf("confirm button must be disabled until the word is typed")
	}
	test.Type(focused, "unlin")
	if !confirm.Disabled() {
		t.Fatalf("partial word must not enable confirm")
	}
	test.Type(focused, "k")
	if confirm.Disabled() {
		t.Fatalf("exact word must enable confirm")
	}
	test.Tap(confirm)
	r.waitFor(t, func() bool {
		r.client.mu.Lock()
		defer r.client.mu.Unlock()
		return r.client.unlinks == 1
	})
	r.waitFor(t, func() bool { return len(r.nav.routes()) == 1 })
	if r.nav.routes()[0] != model.RouteTelegram {
		t.Fatalf("navigated = %v", r.nav.routes())
	}
	for _, route := range gatedRoutes() {
		if r.nav.reasonFor(route) != "Link Telegram first" {
			t.Fatalf("route %q not gated: %v", route, r.nav.disabled)
		}
	}
}

func walkOverlay(o fyne.CanvasObject, fn func(fyne.CanvasObject)) {
	walk(o, fn)
	if r, ok := o.(fyne.Widget); ok {
		for _, child := range test.WidgetRenderer(r).Objects() {
			walkOverlay(child, fn)
		}
	}
}

type confirmButton interface {
	fyne.Tappable
	fyne.Disableable
}

func overlayConfirm(win fyne.Window) confirmButton {
	top := win.Canvas().Overlays().Top()
	if top == nil {
		return nil
	}
	var last confirmButton
	walkOverlay(top, func(co fyne.CanvasObject) {
		if b, ok := co.(confirmButton); ok {
			last = b
		}
	})
	return last
}

func TestRevokeSelfFlowRoutesToConnect(t *testing.T) {
	r := newRig(t, false)
	r.vm.RefreshDevices(context.Background())
	r.screen.view.showStep(stepDevices, true)
	r.queueUI()
	test.Tap(findButton(r.screen.view.root, "Revoke…"))
	confirm := overlayConfirm(r.win)
	if confirm == nil {
		t.Fatalf("no confirm dialog")
	}
	test.Tap(confirm)
	r.waitFor(t, func() bool { return len(r.nav.routes()) == 1 })
	if r.nav.routes()[0] != model.RouteConnect {
		t.Fatalf("navigated = %v", r.nav.routes())
	}
	if _, err := r.tokens.Get("srv"); err == nil {
		t.Fatalf("token kept after revoking this device")
	}
}

func TestRevokeLastDeviceAsksAgainInView(t *testing.T) {
	r := newRig(t, false)
	r.client.revokeErrs = []error{&adminv1.APIError{Status: 409, Body: adminv1.ErrorBody{Code: adminv1.CodeLastDevice}}}
	r.vm.RefreshDevices(context.Background())
	r.screen.view.showStep(stepDevices, true)
	r.queueUI()
	test.Tap(findButton(r.screen.view.root, "Revoke…"))
	first := overlayConfirm(r.win)
	test.Tap(first)
	var second confirmButton
	r.waitFor(t, func() bool {
		top := r.win.Canvas().Overlays().Top()
		if top == nil {
			return false
		}
		second = overlayConfirm(r.win)
		return second != nil && second != first
	})
	test.Tap(second)
	r.waitFor(t, func() bool {
		r.client.mu.Lock()
		defer r.client.mu.Unlock()
		return len(r.client.revokeCalls) == 2
	})
	if r.client.revokeCalls[0] || !r.client.revokeCalls[1] {
		t.Fatalf("confirm_last flags = %v", r.client.revokeCalls)
	}
}

func TestPreferencesPersistThroughControls(t *testing.T) {
	r := newRig(t, false)
	r.screen.view.showStep(stepPreferences, true)
	prefs := r.screen.view.entries[6].panel.(*preferencesPanel)
	prefs.theme.SetSelected(vm.ThemeDark)
	if r.prefs.strings[vm.PrefTheme] != vm.ThemeDark {
		t.Fatalf("theme pref = %q", r.prefs.strings[vm.PrefTheme])
	}
	prefs.concurrency.SetSelected("3")
	if r.prefs.ints[vm.PrefUploadConcurrency] != 3 {
		t.Fatalf("concurrency pref = %d", r.prefs.ints[vm.PrefUploadConcurrency])
	}
	prefs.clipboard.SetText("30")
	prefs.onClipboard()
	if r.prefs.ints[vm.PrefClipboardClearSeconds] != 30 {
		t.Fatalf("clipboard pref = %d", r.prefs.ints[vm.PrefClipboardClearSeconds])
	}
	prefs.clipboard.SetText("abc")
	prefs.onClipboard()
	if p := r.vm.State().Get().Problem; p == nil || p.Field != "clipboard_clear_seconds" {
		t.Fatalf("Problem = %+v", p)
	}
	if !strings.Contains(visibleTexts(r.screen.view.root), "seconds must be 0 or more") {
		t.Fatalf("inline error not shown")
	}
	prefs.downloadDir.SetText("/data/dl")
	prefs.onDownloadDir()
	if r.prefs.strings[vm.PrefDownloadDir] != "/data/dl" {
		t.Fatalf("download dir pref = %q", r.prefs.strings[vm.PrefDownloadDir])
	}
}

func TestActivityFilterSendsPrefix(t *testing.T) {
	r := newRig(t, false)
	r.screen.view.showStep(stepActivity, true)
	r.queueUI()
	panel := r.screen.view.entries[4].panel.(*activityPanel)
	loaded := make(chan struct{}, 4)
	cancel := r.vm.State().Subscribe(func(s vm.State) {
		if !s.Loading && s.Activity.Loaded {
			loaded <- struct{}{}
		}
	})
	defer cancel()
	panel.filter.SetSelected("bucket.")
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatalf("filter did not load")
	}
	r.client.mu.Lock()
	q := r.client.audits[0]
	r.client.mu.Unlock()
	if q.Action != "bucket." || q.Limit != 100 {
		t.Fatalf("query = %+v", q)
	}
	r.queue.drain()
	text := visibleTexts(r.screen.view.root)
	for _, want := range []string{"bucket.create", "photos", "Office laptop", "Load more"} {
		if !strings.Contains(text, want) {
			t.Errorf("activity lacks %q", want)
		}
	}
}

func TestProblemsGoThroughNotifier(t *testing.T) {
	r := newRig(t, true)
	r.vm.Unlink(context.Background(), "wrong")
	if len(r.notif.problems) != 0 {
		t.Fatalf("unconfirmed unlink must not raise a problem")
	}
	r.vm.RenameServer("")
	if len(r.notif.problems) != 0 {
		t.Fatalf("inline problem must stay inline: %+v", r.notif.problems)
	}
	if !strings.Contains(visibleTexts(r.screen.view.root), "names are 1 to 64 characters") {
		t.Fatalf("inline strip missing")
	}
	r.vm.SetTheme(vm.ThemeDark)
	r.vm.RotateToken(context.Background())
	if len(r.notif.infos) != 1 || r.notif.infos[0] != vm.NoticeTokenRotated {
		t.Fatalf("infos = %v", r.notif.infos)
	}
}

func TestThemeFor(t *testing.T) {
	test.NewTempApp(t)
	tests := []struct {
		mode string
		same bool
	}{
		{vm.ThemeSystem, true},
		{vm.ThemeLight, false},
		{vm.ThemeDark, false},
		{"unknown", true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := ThemeFor(tt.mode)
			_, forced := got.(forcedVariantTheme)
			if forced == tt.same {
				t.Fatalf("forced = %v", forced)
			}
		})
	}
	light := ThemeFor(vm.ThemeLight).Color(theme.ColorNameBackground, theme.VariantDark)
	dark := ThemeFor(vm.ThemeDark).Color(theme.ColorNameBackground, theme.VariantLight)
	lr, lg, lb, _ := light.RGBA()
	dr, dg, db, _ := dark.RGBA()
	if lr == dr && lg == dg && lb == db {
		t.Fatalf("forced variants produce the same background")
	}
	if ThemeFor(vm.ThemeDark).Size(theme.SizeNamePadding) != theme.DefaultTheme().Size(theme.SizeNamePadding) {
		t.Fatalf("sizes must pass through")
	}
}

func TestNewViewWithoutNotifierFindsWindow(t *testing.T) {
	test.NewTempApp(t)
	c := &fakeClient{}
	v := vm.New(c, func() time.Time { return testNow }, vm.Session{})
	content := NewView(v, &fakeNav{})
	win := test.NewTempWindow(t, content)
	if win == nil {
		t.Fatalf("no window")
	}
	if got := visibleTexts(content); !strings.Contains(got, "Connection") {
		t.Fatalf("content = %q", got)
	}
}
