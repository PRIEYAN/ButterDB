package settings

import (
	"context"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClient struct {
	mu     sync.Mutex
	audits []adminv1.AuditQuery
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	return adminv1.Status{ServerVersion: "0.9.0"}, nil
}

func (f *fakeClient) TelegramStatus(context.Context) (adminv1.TelegramStatus, error) {
	return adminv1.TelegramStatus{
		State:   adminv1.TelegramLinked,
		Account: &adminv1.TelegramAccount{FirstName: "Ada", Username: "ada", PhoneMasked: "+49 *** 12", Connected: true, LinkedAt: testNow.Add(-72 * time.Hour)},
	}, nil
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

type fakeAccount struct {
	mu      sync.Mutex
	removes int
	renames []string
}

func (a *fakeAccount) Current() (vm.Project, bool) {
	return vm.Project{ID: "p1", Name: "photos", LiveBuckets: 3}, true
}

func (a *fakeAccount) Rename(_ context.Context, _ vm.ProjectID, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.renames = append(a.renames, name)
	return nil
}

func (a *fakeAccount) ChangePassword(context.Context, vm.ProjectID, []byte, []byte) error {
	return nil
}

func (a *fakeAccount) ResetKey(context.Context, vm.ProjectID, []byte, string) (vm.ResetReport, error) {
	return vm.ResetReport{UnreadableObjects: map[string]int64{"backups": 4}, RevokedAccessKeys: 2}, nil
}

func (a *fakeAccount) Logout(context.Context) error { return nil }

func (a *fakeAccount) RemoveDevice(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removes++
	return nil
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
	account *fakeAccount
	nav     *fakeNav
	notif   *fakeNotifier
	prefs   *memPrefs
	win     fyne.Window
	queue   *uiQueue
}

func newRig(t *testing.T, withNotifier bool) *rig {
	t.Helper()
	test.NewTempApp(t)
	r := &rig{
		client:  &fakeClient{},
		account: &fakeAccount{},
		nav:     &fakeNav{},
		notif:   &fakeNotifier{},
		prefs:   &memPrefs{strings: map[string]string{}, ints: map[string]int{}},
	}
	r.vm = vm.New(r.client, func() time.Time { return testNow }, vm.Session{
		Account: r.account, Preferences: r.prefs, AppVersion: "0.1.0", DefaultDownloadDir: "/home/me/Downloads",
	})
	r.win = test.NewTempWindow(t, widget.NewLabel("placeholder"))
	if withNotifier {
		r.screen = NewScreen(r.vm, r.nav, r.notif, &fakeClipboard{}, r.win)
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
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(c).Objects() {
			walk(child, fn)
		}
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
		case *shell.Badge:
			out = append(out, w.Text())
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

func TestShowSelectsStepFromParams(t *testing.T) {
	tests := []struct {
		name   string
		params model.Params
		want   string
	}{
		{"f1 opens about", model.Params{model.ParamStep: "about"}, stepAbout},
		{"unknown step is ignored", model.Params{model.ParamStep: "nope"}, stepProject},
		{"no step keeps the first section", nil, stepProject},
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

func TestPreferencesPersistThroughControls(t *testing.T) {
	r := newRig(t, false)
	r.screen.view.showStep(stepPreferences, true)
	prefs := r.screen.view.entries[4].panel.(*preferencesPanel)
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
	panel := r.screen.view.entries[2].panel.(*activityPanel)
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

func TestThemeFor(t *testing.T) {
	test.NewTempApp(t)
	parchment := color.NRGBA{R: 0xF7, G: 0xF4, B: 0xEB, A: 0xff}
	terminal := color.NRGBA{R: 0x11, G: 0x12, B: 0x14, A: 0xff}
	tests := []struct {
		mode        string
		onLightOS   color.NRGBA
		onDarkOS    color.NRGBA
		description string
	}{
		{mode: vm.ThemeSystem, onLightOS: parchment, onDarkOS: terminal, description: "system follows the OS variant"},
		{mode: vm.ThemeLight, onLightOS: parchment, onDarkOS: parchment, description: "light forces parchment"},
		{mode: vm.ThemeDark, onLightOS: terminal, onDarkOS: terminal, description: "dark forces terminal"},
		{mode: "unknown", onLightOS: parchment, onDarkOS: terminal, description: "unknown falls back to system"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := ThemeFor(tt.mode)
			if c := color.NRGBAModel.Convert(got.Color(theme.ColorNameBackground, theme.VariantLight)); c != tt.onLightOS {
				t.Fatalf("%s: background on a light OS = %v, want %v", tt.description, c, tt.onLightOS)
			}
			if c := color.NRGBAModel.Convert(got.Color(theme.ColorNameBackground, theme.VariantDark)); c != tt.onDarkOS {
				t.Fatalf("%s: background on a dark OS = %v, want %v", tt.description, c, tt.onDarkOS)
			}
		})
	}
	if ThemeFor(vm.ThemeDark).Size(theme.SizeNamePadding) != theme.DefaultTheme().Size(theme.SizeNamePadding) {
		t.Fatalf("the base padding must stay on Fyne's grid")
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
	if got := visibleTexts(content); !strings.Contains(got, "Project") {
		t.Fatalf("content = %q", got)
	}
}

func TestRendersEverySection(t *testing.T) {
	r := newRig(t, false)
	r.vm.Refresh(context.Background())
	text := visibleTexts(r.screen.view.root)
	for _, step := range []string{stepTelegram, stepAbout, stepPreferences, stepProject} {
		r.screen.view.showStep(step, true)
		text += "\n" + visibleTexts(r.screen.view.root)
	}
	for _, want := range []string{
		"Project ID", "p1", "Rename project", "Change project password", "Reset project key", "Switch project",
		"Ada", "@ada", "+49 *** 12", "Log out of Telegram", "Remove this account from this device…", safetyNotice,
		"Fyne", "BSD-3-Clause", "0.1.0", "0.9.0",
		"Theme", "Upload concurrency (1 to 4)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered text lacks %q", want)
		}
	}
}

func TestRemoveDeviceAsksFirstThenRoutesToTelegram(t *testing.T) {
	r := newRig(t, false)
	r.screen.view.showStep(stepTelegram, true)
	r.queueUI()
	test.Tap(findButton(r.screen.view.root, "Remove this account from this device…"))
	confirm := overlayConfirm(r.win)
	if confirm == nil {
		t.Fatalf("no confirmation dialog")
	}
	r.account.mu.Lock()
	early := r.account.removes
	r.account.mu.Unlock()
	if early != 0 {
		t.Fatalf("device removed before confirming")
	}
	test.Tap(confirm)
	r.waitFor(t, func() bool { return len(r.nav.routes()) == 1 })
	if r.nav.routes()[0] != model.RouteTelegram {
		t.Fatalf("navigated = %v", r.nav.routes())
	}
	if r.account.removes != 1 {
		t.Fatalf("removes = %d", r.account.removes)
	}
}

func walkOverlay(o fyne.CanvasObject, fn func(fyne.CanvasObject)) {
	walk(o, fn)
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
