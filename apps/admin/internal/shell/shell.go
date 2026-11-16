package shell

import (
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	windowTitle     = "butterDB Admin"
	minWindowWidth  = 1024
	minWindowHeight = 680
	closeLabel      = "Close"
	labelOffline    = "Offline"
	labelConnected  = "Connected"
)

var sidebarOrder = []model.Route{
	model.RouteTelegram,
	model.RouteDashboard,
	model.RouteBuckets,
	model.RouteKeys,
	model.RouteObjects,
	model.RouteReplica,
	model.RouteSettings,
}

type Shell struct {
	app  fyne.App
	win  fyne.Window
	clip model.Clipboard
	now  func() time.Time

	sidebar   *sidebar
	banner    *banner
	content   *fyne.Container
	serverTxt *widget.Label
	linkTxt   *widget.Label
	linkIcon  *widget.Icon

	mu            sync.Mutex
	screens       map[model.Route]Screen
	order         []model.Route
	states        map[model.Route]routeState
	shortcuts     map[model.Route]*desktop.CustomShortcut
	current       model.Route
	currentScreen Screen
	currentParams model.Params
	cancel        context.CancelFunc
}

func New(app fyne.App, win fyne.Window) *Shell {
	s := &Shell{
		app:       app,
		win:       win,
		clip:      appClipboard{app: app},
		now:       time.Now,
		screens:   make(map[model.Route]Screen),
		states:    make(map[model.Route]routeState),
		shortcuts: make(map[model.Route]*desktop.CustomShortcut),
	}
	s.build()
	s.registerShellShortcuts()
	s.SetServerLabel("", false)
	return s
}

func (s *Shell) build() {
	s.sidebar = newSidebar(func(r model.Route) { s.navigate(r, nil) }, s.switchServer)
	s.banner = newBanner(s.clip, func() time.Time { return s.now() }, s.refreshCurrent)
	s.content = container.NewStack()
	s.serverTxt = widget.NewLabel("")
	s.linkTxt = widget.NewLabel(labelOffline)
	s.linkIcon = widget.NewIcon(theme.NewErrorThemedResource(theme.CancelIcon()))
	statusLine := container.NewHBox(s.linkIcon, s.linkTxt, widget.NewSeparator(), s.serverTxt)
	minimum := canvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	minimum.SetMinSize(fyne.NewSize(minWindowWidth, minWindowHeight))
	body := container.NewBorder(s.banner.object(), statusLine, s.sidebar.root, nil, s.content)
	s.win.SetContent(container.NewStack(minimum, body))
}

func (s *Shell) registerShellShortcuts() {
	cv := s.win.Canvas()
	cv.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyR, Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) {
		s.refreshCurrent()
	})
	cv.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyW, Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) {
		s.switchServer()
	})
	cv.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) {
		s.app.Quit()
	})
	cv.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyF1}, func(fyne.Shortcut) {
		s.navigate(model.RouteSettings, model.Params{model.ParamStep: "about"})
	})
}

func (s *Shell) Register(sc Screen) {
	route := sc.Route()
	s.mu.Lock()
	_, replacing := s.screens[route]
	wasCurrent := s.current == route
	s.mu.Unlock()
	if replacing && wasCurrent {
		s.leaveCurrent()
	}
	s.mu.Lock()
	if !replacing {
		s.order = append(s.order, route)
	}
	s.screens[route] = sc
	s.mu.Unlock()
	s.bindShortcut(route, sc.Shortcut())
	s.refreshSidebar()
}

func (s *Shell) bindShortcut(route model.Route, key fyne.KeyName) {
	if old, ok := s.shortcuts[route]; ok {
		s.win.Canvas().RemoveShortcut(old)
		delete(s.shortcuts, route)
	}
	if key == "" {
		return
	}
	shortcut := &desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierShortcutDefault}
	s.shortcuts[route] = shortcut
	s.win.Canvas().AddShortcut(shortcut, func(fyne.Shortcut) { s.navigate(route, nil) })
}

func (s *Shell) Navigator() Navigator {
	return navigator{s: s}
}

func (s *Shell) Notifier() Notifier {
	return notifier{s: s}
}

func (s *Shell) Window() fyne.Window {
	return s.win
}

func (s *Shell) SetServerLabel(label string, connected bool) {
	s.serverTxt.SetText(label)
	if label == "" {
		s.win.SetTitle(windowTitle)
	} else {
		s.win.SetTitle(windowTitle + " — " + label)
	}
	if connected {
		s.linkTxt.SetText(labelConnected)
		s.linkIcon.SetResource(theme.NewSuccessThemedResource(theme.ConfirmIcon()))
		return
	}
	s.linkTxt.SetText(labelOffline)
	s.linkIcon.SetResource(theme.NewErrorThemedResource(theme.CancelIcon()))
}

func (s *Shell) Reset() {
	s.leaveCurrent()
	s.mu.Lock()
	routes := make([]model.Route, 0, len(s.shortcuts))
	for route := range s.shortcuts {
		routes = append(routes, route)
	}
	s.screens = make(map[model.Route]Screen)
	s.order = nil
	s.states = make(map[model.Route]routeState)
	s.mu.Unlock()
	for _, route := range routes {
		s.bindShortcut(route, "")
	}
	s.banner.clear()
	s.sidebar.show(true)
	s.content.Objects = nil
	s.content.Refresh()
	s.refreshSidebar()
	s.SetServerLabel("", false)
}

func (s *Shell) currentRoute() model.Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *Shell) navigate(r model.Route, p model.Params) {
	s.mu.Lock()
	sc, ok := s.screens[r]
	disabled := s.states[r].disabled
	s.mu.Unlock()
	if !ok || disabled {
		return
	}
	s.leaveCurrent()
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.current, s.currentScreen, s.currentParams, s.cancel = r, sc, p, cancel
	s.mu.Unlock()
	s.banner.clear()
	s.content.Objects = []fyne.CanvasObject{sc.Content()}
	s.content.Refresh()
	s.sidebar.show(r != model.RouteConnect)
	s.sidebar.markCurrent(r)
	sc.Show(ctx, p)
}

func (s *Shell) leaveCurrent() {
	s.mu.Lock()
	prev, cancel := s.currentScreen, s.cancel
	s.current, s.currentScreen, s.currentParams, s.cancel = "", nil, nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if prev != nil {
		prev.Hide()
	}
}

func (s *Shell) refreshCurrent() {
	s.mu.Lock()
	route, params := s.current, s.currentParams
	s.mu.Unlock()
	if route == "" {
		return
	}
	s.navigate(route, params)
}

func (s *Shell) switchServer() {
	s.navigate(model.RouteConnect, nil)
}

func (s *Shell) setEnabled(r model.Route, enabled bool, reason string) {
	s.mu.Lock()
	s.states[r] = routeState{disabled: !enabled, reason: reason}
	s.mu.Unlock()
	s.refreshSidebar()
}

func (s *Shell) refreshSidebar() {
	s.mu.Lock()
	entries := make([]sidebarEntry, 0, len(s.order))
	for _, route := range s.orderedRoutes() {
		state := s.states[route]
		if state.disabled && state.reason == "" {
			continue
		}
		sc := s.screens[route]
		entries = append(entries, sidebarEntry{route: route, title: sc.Title(), icon: sc.Icon(), disabled: state.disabled, reason: state.reason})
	}
	current := s.current
	s.mu.Unlock()
	s.sidebar.setEntries(entries, current)
}

func (s *Shell) orderedRoutes() []model.Route {
	routes := make([]model.Route, 0, len(s.order))
	seen := make(map[model.Route]bool, len(s.order))
	for _, route := range sidebarOrder {
		if _, ok := s.screens[route]; ok {
			routes = append(routes, route)
			seen[route] = true
		}
	}
	for _, route := range s.order {
		if route != model.RouteConnect && !seen[route] {
			routes = append(routes, route)
		}
	}
	return routes
}

func (s *Shell) present(p model.Problem) {
	switch p.Presentation {
	case model.PresentBanner:
		s.banner.showProblem(p)
	case model.PresentDialog, model.PresentInline:
		s.showProblemDialog(p)
	case model.PresentRoute:
		s.showProblemDialog(p)
		if p.Route != "" {
			s.navigate(p.Route, nil)
		}
	}
}

func (s *Shell) showProblemDialog(p model.Problem) {
	showProblemDialog(s.win, NewProblemView(p, s.clip))
}

type navigator struct{ s *Shell }

func (n navigator) Navigate(r model.Route, p model.Params) { n.s.navigate(r, p) }

func (n navigator) Current() model.Route { return n.s.currentRoute() }

func (n navigator) SetEnabled(r model.Route, enabled bool, reason string) {
	n.s.setEnabled(r, enabled, reason)
}

type notifier struct{ s *Shell }

func (n notifier) Problem(p model.Problem) { n.s.present(p) }

func (n notifier) Info(text string) { n.s.banner.showInfo(text) }

func (n notifier) ClearBanner() { n.s.banner.clear() }
