package app

import (
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

func reflectType(v any) string { return reflect.TypeOf(v).String() }

func TestStartupNotLinkedGoesToTelegram(t *testing.T) {
	h := newHarness(t)
	h.engine.seedProject("photos", embedded.ProjectReady, testProjectPassword)
	h.start()

	h.waitRoute(model.RouteTelegram)
	h.waitEntry("digits only")
	if h.engine.isOpen() {
		t.Fatalf("a project opened before Telegram login")
	}
	nav := h.comp.sh.Navigator()
	for _, route := range []model.Route{model.RouteDashboard, model.RouteBuckets, model.RouteObjects, model.RouteSettings} {
		nav.Navigate(route, nil)
		h.settle()
		if got := h.route(); got != model.RouteTelegram {
			t.Fatalf("navigating to %q without a project ended on %q", route, got)
		}
	}
}

func TestStartupLinkedGoesToProjects(t *testing.T) {
	h := newHarness(t, withLinked())
	h.engine.seedProject("photos", embedded.ProjectReady, testProjectPassword)
	h.engine.seedProject("archive", embedded.ProjectLocked, testProjectPassword)
	h.start()

	h.waitRoute(model.RouteProjects)
	h.waitLabel("photos")
	h.waitLabel("archive")
	h.waitButton("Open")
	h.waitButton("Unlock")
	if h.engine.isOpen() {
		t.Fatalf("a project opened without the user choosing one")
	}
}

func TestOpeningProjectShowsItsScreens(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	nav := h.comp.sh.Navigator()
	for _, route := range []model.Route{model.RouteBuckets, model.RouteObjects, model.RouteSettings, model.RouteDashboard} {
		nav.Navigate(route, nil)
		h.settle()
		if got := h.route(); got != route {
			t.Fatalf("navigate to %q ended on %q", route, got)
		}
	}
	nav.Navigate(model.RouteKeys, nil)
	h.settle()
	if got := h.route(); got != model.RouteDashboard {
		t.Fatalf("access keys screen is reachable, route %q", got)
	}
	if got := h.win.Title(); got != "butterDB Admin — photos" {
		t.Fatalf("window title = %q", got)
	}
}

func TestUnlockLockedProjectOpensIt(t *testing.T) {
	h := newHarness(t, withLinked())
	h.engine.seedProject("archive", embedded.ProjectLocked, testProjectPassword)
	h.start()
	h.waitRoute(model.RouteProjects)

	test.Tap(h.waitButton("Unlock"))
	h.waitLabel("Unlock archive")
	test.Type(h.waitEntry("Password"), "not the password")
	test.Tap(h.waitButton("Unlock"))
	h.waitLabel("That password does not open this project.")
	if got := h.route(); got != model.RouteProjects || h.engine.isOpen() {
		t.Fatalf("wrong password left the picker: route %q open %v", got, h.engine.isOpen())
	}
	test.Type(h.waitEntry("Password"), testProjectPassword)
	test.Tap(h.waitButton("Unlock"))
	h.waitRoute(model.RouteDashboard)
}

func TestSwitchProjectClosesProjectAndCancelsSession(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	session := h.comp.currentSession()
	if session == nil || session.ctx.Err() != nil {
		t.Fatalf("no live session after opening a project")
	}

	h.typeShortcut(fyne.KeyW)
	h.waitRoute(model.RouteProjects)

	if session.ctx.Err() == nil {
		t.Fatalf("session context still live after switching project")
	}
	if h.comp.currentSession() != nil {
		t.Fatalf("session still registered after switching project")
	}
	if h.engine.isOpen() || h.engine.closeCount() != 1 {
		t.Fatalf("project open %v, closes %d", h.engine.isOpen(), h.engine.closeCount())
	}
	h.comp.sh.Navigator().Navigate(model.RouteDashboard, nil)
	h.settle()
	if got := h.route(); got != model.RouteProjects {
		t.Fatalf("dashboard stayed registered after switching project, route %q", got)
	}
	if got := h.win.Title(); got != "butterDB Admin" {
		t.Fatalf("window title after switching = %q", got)
	}
}

func TestLogoutFromSettingsReturnsToTelegram(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	h.comp.sh.Navigator().Navigate(model.RouteSettings, model.Params{model.ParamStep: "telegram"})
	test.Tap(h.waitButton("Log out of Telegram"))
	h.waitRoute(model.RouteTelegram)
	if h.engine.isOpen() || h.comp.currentSession() != nil {
		t.Fatalf("project still open after logging out")
	}
}

func TestNoScreenAsksForAHostAddress(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	routes := []model.Route{model.RouteDashboard, model.RouteBuckets, model.RouteObjects}
	steps := []string{"project", "telegram", "activity", "about", "preferences"}
	check := func(where string) {
		for _, o := range visibleObjects(h.content()) {
			if e, ok := o.(*widget.Entry); ok && e.PlaceHolder == "host or host:port" {
				t.Fatalf("%s shows an address entry", where)
			}
		}
	}
	nav := h.comp.sh.Navigator()
	for _, route := range routes {
		nav.Navigate(route, nil)
		h.settle()
		check(string(route))
	}
	for _, step := range steps {
		nav.Navigate(model.RouteSettings, model.Params{model.ParamStep: step})
		h.settle()
		check("settings/" + step)
	}
	nav.Navigate(model.RouteProjects, nil)
	h.waitRoute(model.RouteProjects)
	h.waitButton("New project")
	check("projects")
	test.Tap(h.waitButton("New project"))
	h.waitEntry("Project name")
	check("projects/new")
	h.admin.logout()
	nav.Navigate(model.RouteTelegram, nil)
	h.waitRoute(model.RouteTelegram)
	h.settle()
	check("telegram")
}

func TestThemeAppliedAtStartup(t *testing.T) {
	h := newHarness(t)
	h.app.Preferences().SetString(settingsvm.PrefTheme, settingsvm.ThemeDark)
	h.start()

	got := h.app.Settings().Theme()
	if got == nil {
		t.Fatalf("no theme set")
	}
	want := settingsui.ThemeFor(settingsvm.ThemeDark)
	if reflectType(got) != reflectType(want) {
		t.Fatalf("theme type %s, want %s", reflectType(got), reflectType(want))
	}
}
