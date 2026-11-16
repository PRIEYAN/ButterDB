package app

import (
	"testing"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

func TestRoutingUnlinkedGoesToTelegram(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withClaimed())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()

	h.waitRoute(model.RouteTelegram)
	nav := h.comp.sh.Navigator()
	for _, gated := range gatedRoutes() {
		nav.Navigate(gated, nil)
		h.settle()
		if got := h.route(); got != model.RouteTelegram {
			t.Fatalf("navigating to disabled %q ended on %q, want telegram", gated, got)
		}
	}
	nav.Navigate(model.RouteSettings, nil)
	h.settle()
	if got := h.route(); got != model.RouteSettings {
		t.Fatalf("settings must stay reachable while unlinked, got %q", got)
	}
}

func TestRoutingLinkedGoesToDashboard(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()

	h.waitRoute(model.RouteDashboard)
	nav := h.comp.sh.Navigator()
	for _, route := range []model.Route{model.RouteBuckets, model.RouteKeys, model.RouteObjects, model.RouteSettings, model.RouteDashboard} {
		nav.Navigate(route, nil)
		h.settle()
		if got := h.route(); got != route {
			t.Fatalf("navigate to %q ended on %q", route, got)
		}
	}
	nav.Navigate(model.RouteTelegram, nil)
	h.settle()
	if got := h.route(); got != model.RouteDashboard {
		t.Fatalf("telegram route must be hidden once linked, got %q", got)
	}
	if got := h.win.Title(); got != "butterDB Admin — home server" {
		t.Fatalf("window title = %q", got)
	}
}

func TestRoutingRestoresLastRoute(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.app.Preferences().SetString(settingsvm.LastRouteKey(testServerID), string(model.RouteKeys))
	h.start()

	h.waitRoute(model.RouteKeys)
}

func TestRoutingIgnoresInvalidLastRoute(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.app.Preferences().SetString(settingsvm.LastRouteKey(testServerID), string(model.RouteConnect))
	h.start()

	h.waitRoute(model.RouteDashboard)
}

func TestNavigationPersistsLastRoutePerServer(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)

	h.typeShortcut(fyne.Key2)
	h.waitRoute(model.RouteBuckets)
	key := settingsvm.LastRouteKey(testServerID)
	if got := h.app.Preferences().String(key); got != string(model.RouteBuckets) {
		t.Fatalf("stored last route after shortcut = %q", got)
	}
	h.comp.sh.Navigator().Navigate(model.RouteSettings, nil)
	h.settle()
	if got := h.app.Preferences().String(key); got != string(model.RouteSettings) {
		t.Fatalf("stored last route after navigate = %q", got)
	}
}

func TestSwitchServerCancelsSession(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)

	session := h.comp.currentSession()
	if session == nil {
		t.Fatalf("no session after connecting")
	}
	if session.ctx.Err() != nil {
		t.Fatalf("session context cancelled while connected")
	}

	h.typeShortcut(fyne.KeyW)
	h.waitRoute(model.RouteConnect)

	select {
	case <-session.ctx.Done():
	default:
		t.Fatalf("session context still live after switching server")
	}
	if h.comp.currentSession() != nil {
		t.Fatalf("session still registered after switching server")
	}
	h.comp.sh.Navigator().Navigate(model.RouteDashboard, nil)
	h.settle()
	if got := h.route(); got != model.RouteConnect {
		t.Fatalf("dashboard stayed registered after switching server, route %q", got)
	}
	if got := h.win.Title(); got != "butterDB Admin" {
		t.Fatalf("window title after switching = %q", got)
	}
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
