package shell

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type fakeScreen struct {
	route    model.Route
	title    string
	shortcut fyne.KeyName
	content  fyne.CanvasObject
	shown    []model.Params
	ctxs     []context.Context
	hidden   int
}

func newFakeScreen(route model.Route, shortcut fyne.KeyName) *fakeScreen {
	return &fakeScreen{route: route, title: string(route), shortcut: shortcut, content: widget.NewLabel(string(route))}
}

func (f *fakeScreen) Route() model.Route         { return f.route }
func (f *fakeScreen) Title() string              { return f.title }
func (f *fakeScreen) Icon() fyne.Resource        { return theme.HomeIcon() }
func (f *fakeScreen) Shortcut() fyne.KeyName     { return f.shortcut }
func (f *fakeScreen) Content() fyne.CanvasObject { return f.content }
func (f *fakeScreen) Show(ctx context.Context, p model.Params) {
	f.ctxs = append(f.ctxs, ctx)
	f.shown = append(f.shown, p)
}
func (f *fakeScreen) Hide() { f.hidden++ }

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

func newTestShell(t *testing.T) (*Shell, fyne.Window) {
	t.Helper()
	app := test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("placeholder"))
	s := New(app, win)
	s.clip = &fakeClipboard{}
	s.now = func() time.Time { return time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC) }
	return s, win
}

func typeShortcut(t *testing.T, win fyne.Window, key fyne.KeyName, modifier fyne.KeyModifier) {
	t.Helper()
	handler, ok := win.Canvas().(interface{ TypedShortcut(fyne.Shortcut) })
	if !ok {
		t.Fatalf("test canvas does not dispatch shortcuts")
	}
	handler.TypedShortcut(&desktop.CustomShortcut{KeyName: key, Modifier: modifier})
}

func TestNavigationShowsAndHidesScreens(t *testing.T) {
	s, _ := newTestShell(t)
	dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
	buckets := newFakeScreen(model.RouteBuckets, fyne.Key2)
	s.Register(dashboard)
	s.Register(buckets)
	nav := s.Navigator()

	nav.Navigate(model.RouteDashboard, nil)
	if nav.Current() != model.RouteDashboard || len(dashboard.shown) != 1 {
		t.Fatalf("Current = %q, dashboard shown %d times", nav.Current(), len(dashboard.shown))
	}
	dashboardCtx := dashboard.ctxs[0]
	if dashboardCtx.Err() != nil {
		t.Fatalf("dashboard context cancelled while visible")
	}

	nav.Navigate(model.RouteBuckets, model.Params{model.ParamBucket: "photos"})
	if dashboardCtx.Err() == nil {
		t.Fatalf("dashboard context still live after navigating away")
	}
	if dashboard.hidden != 1 {
		t.Fatalf("dashboard hidden %d times, want 1", dashboard.hidden)
	}
	if nav.Current() != model.RouteBuckets || len(buckets.shown) != 1 {
		t.Fatalf("Current = %q, buckets shown %d times", nav.Current(), len(buckets.shown))
	}
	if got := buckets.shown[0][model.ParamBucket]; got != "photos" {
		t.Fatalf("buckets params bucket = %q, want photos", got)
	}
	if buckets.ctxs[0].Err() != nil {
		t.Fatalf("buckets context cancelled while visible")
	}
}

func TestNavigateAgainRestartsScreenWithFreshContext(t *testing.T) {
	s, _ := newTestShell(t)
	buckets := newFakeScreen(model.RouteBuckets, fyne.Key2)
	s.Register(buckets)
	nav := s.Navigator()
	nav.Navigate(model.RouteBuckets, nil)
	nav.Navigate(model.RouteBuckets, model.Params{model.ParamBucket: "b"})
	if len(buckets.ctxs) != 2 || buckets.ctxs[0].Err() == nil || buckets.ctxs[1].Err() != nil {
		t.Fatalf("contexts after re-navigation: %d, first cancelled %v", len(buckets.ctxs), buckets.ctxs[0].Err() != nil)
	}
	if buckets.hidden != 1 {
		t.Fatalf("hidden = %d, want 1", buckets.hidden)
	}
}

func TestNavigationIgnoresUnknownAndDisabledRoutes(t *testing.T) {
	s, _ := newTestShell(t)
	dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
	buckets := newFakeScreen(model.RouteBuckets, fyne.Key2)
	s.Register(dashboard)
	s.Register(buckets)
	nav := s.Navigator()
	nav.Navigate(model.RouteDashboard, nil)

	cases := []struct {
		name  string
		setup func()
		route model.Route
	}{
		{name: "unknown route", setup: func() {}, route: model.RouteObjects},
		{name: "disabled route", setup: func() { nav.SetEnabled(model.RouteBuckets, false, "Link Telegram first") }, route: model.RouteBuckets},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			nav.Navigate(tc.route, nil)
			if nav.Current() != model.RouteDashboard {
				t.Fatalf("Current = %q, want dashboard", nav.Current())
			}
			if dashboard.hidden != 0 || dashboard.ctxs[0].Err() != nil {
				t.Fatalf("dashboard was hidden by a refused navigation")
			}
		})
	}

	nav.SetEnabled(model.RouteBuckets, true, "")
	nav.Navigate(model.RouteBuckets, nil)
	if nav.Current() != model.RouteBuckets {
		t.Fatalf("Current = %q, want buckets after re-enable", nav.Current())
	}
}

func TestSidebarOrderAndDisabledReasons(t *testing.T) {
	s, _ := newTestShell(t)
	for _, sc := range []*fakeScreen{
		newFakeScreen(model.RouteSettings, fyne.Key6),
		newFakeScreen(model.RouteProjects, ""),
		newFakeScreen(model.RouteBuckets, fyne.Key2),
		newFakeScreen(model.RouteTelegram, ""),
		newFakeScreen(model.RouteDashboard, fyne.Key1),
	} {
		s.Register(sc)
	}
	nav := s.Navigator()
	nav.SetEnabled(model.RouteDashboard, false, "Link Telegram first")

	var got []model.Route
	for _, e := range s.sidebar.entries {
		got = append(got, e.route)
	}
	want := []model.Route{model.RouteDashboard, model.RouteBuckets, model.RouteSettings}
	if len(got) != len(want) {
		t.Fatalf("sidebar routes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sidebar routes = %v, want %v", got, want)
		}
	}
	if !s.sidebar.entries[0].disabled || s.sidebar.entries[0].reason != "Link Telegram first" {
		t.Fatalf("dashboard entry = %+v", s.sidebar.entries[0])
	}
}

func TestShortcutsNavigateAndRefresh(t *testing.T) {
	s, win := newTestShell(t)
	dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
	buckets := newFakeScreen(model.RouteBuckets, fyne.Key2)
	projects := newFakeScreen(model.RouteProjects, "")
	s.Register(dashboard)
	s.Register(buckets)
	s.Register(projects)

	typeShortcut(t, win, fyne.Key2, fyne.KeyModifierShortcutDefault)
	if s.Navigator().Current() != model.RouteBuckets {
		t.Fatalf("Current = %q after Mod+2, want buckets", s.Navigator().Current())
	}
	typeShortcut(t, win, fyne.Key5, fyne.KeyModifierShortcutDefault)
	if s.Navigator().Current() != model.RouteBuckets {
		t.Fatalf("Mod+5 with no screen changed the route to %q", s.Navigator().Current())
	}
	typeShortcut(t, win, fyne.KeyR, fyne.KeyModifierShortcutDefault)
	if len(buckets.shown) != 2 || buckets.ctxs[0].Err() == nil {
		t.Fatalf("Mod+R: shown %d times, first context cancelled %v", len(buckets.shown), buckets.ctxs[0].Err() != nil)
	}
	typeShortcut(t, win, fyne.KeyW, fyne.KeyModifierShortcutDefault)
	if s.Navigator().Current() != model.RouteProjects {
		t.Fatalf("Current = %q after Mod+W, want projects", s.Navigator().Current())
	}
	if s.sidebar.root.Visible() {
		t.Fatalf("sidebar visible on the project picker")
	}
}

func TestResetCancelsAndUnregistersScreens(t *testing.T) {
	s, win := newTestShell(t)
	dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
	s.Register(dashboard)
	s.Navigator().Navigate(model.RouteDashboard, nil)
	ctx := dashboard.ctxs[0]

	s.Reset()
	if ctx.Err() == nil || dashboard.hidden != 1 {
		t.Fatalf("context cancelled %v, hidden %d", ctx.Err() != nil, dashboard.hidden)
	}
	if s.Navigator().Current() != "" || len(s.sidebar.entries) != 0 {
		t.Fatalf("Current = %q, sidebar entries %d", s.Navigator().Current(), len(s.sidebar.entries))
	}
	typeShortcut(t, win, fyne.Key1, fyne.KeyModifierShortcutDefault)
	s.Navigator().Navigate(model.RouteDashboard, nil)
	if len(dashboard.shown) != 1 {
		t.Fatalf("dashboard shown %d times after reset, want 1", len(dashboard.shown))
	}
}

func TestRegisterReplacesCurrentScreen(t *testing.T) {
	s, _ := newTestShell(t)
	first := newFakeScreen(model.RouteDashboard, fyne.Key1)
	second := newFakeScreen(model.RouteDashboard, fyne.Key1)
	s.Register(first)
	s.Navigator().Navigate(model.RouteDashboard, nil)
	ctx := first.ctxs[0]
	s.Register(second)
	if ctx.Err() == nil || s.Navigator().Current() != "" {
		t.Fatalf("old context cancelled %v, Current = %q", ctx.Err() != nil, s.Navigator().Current())
	}
	s.Navigator().Navigate(model.RouteDashboard, nil)
	if len(second.shown) != 1 || len(first.shown) != 1 {
		t.Fatalf("shown first %d second %d", len(first.shown), len(second.shown))
	}
}

func TestProjectLabelSetsWindowTitle(t *testing.T) {
	s, win := newTestShell(t)
	s.SetProjectLabel("photos")
	if win.Title() != "butterDB Admin — photos" {
		t.Fatalf("title = %q", win.Title())
	}
	if s.linkTxt.Text != "Project open" || s.serverTxt.Text != "photos" || s.sidebar.project.Text != "photos" {
		t.Fatalf("status = %q, %q, sidebar %q", s.linkTxt.Text, s.serverTxt.Text, s.sidebar.project.Text)
	}
	s.SetProjectLabel("")
	if win.Title() != "butterDB Admin" || s.linkTxt.Text != "No project open" {
		t.Fatalf("title = %q, status = %q", win.Title(), s.linkTxt.Text)
	}
	if s.Window() != win {
		t.Fatalf("Window() does not return the constructor window")
	}
}
