package settings

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

const (
	stepConnection  = "connection"
	stepDevice      = "device"
	stepDevices     = "devices"
	stepTelegram    = "telegram"
	stepActivity    = "activity"
	stepAbout       = "about"
	stepPreferences = "preferences"
)

type panel interface {
	object() fyne.CanvasObject
	update(s vm.State)
}

type sectionEntry struct {
	step  string
	title string
	panel panel
}

type view struct {
	vm       *vm.ViewModel
	nav      shell.Navigator
	notifier shell.Notifier
	clip     model.Clipboard
	win      fyne.Window
	post     func(func())

	root    fyne.CanvasObject
	problem *fyne.Container
	notice  *widget.Label
	list    *widget.List
	stack   *fyne.Container
	entries []sectionEntry

	mu          sync.Mutex
	ctx         context.Context
	selected    string
	bannerShown bool
	shownKey    string
	routedKey   string
	noticeKey   string
	syncing     bool
}

func newView(v *vm.ViewModel, nav shell.Navigator, n shell.Notifier, clip model.Clipboard, win fyne.Window) *view {
	w := &view{vm: v, nav: nav, notifier: n, clip: clip, win: win, post: fyne.Do, ctx: context.Background(), selected: stepConnection}
	w.entries = []sectionEntry{
		{stepConnection, "Connection", newConnectionPanel(w)},
		{stepDevice, "This device", newDevicePanel(w)},
		{stepDevices, "Devices", newDevicesPanel(w)},
		{stepTelegram, "Telegram account", newTelegramPanel(w)},
		{stepActivity, "Activity", newActivityPanel(w)},
		{stepAbout, "About", newAboutPanel(w)},
		{stepPreferences, "App preferences", newPreferencesPanel(w)},
	}
	w.build()
	w.render(v.State().Get())
	v.State().Subscribe(func(s vm.State) {
		w.post(func() { w.render(s) })
	})
	return w
}

func (w *view) build() {
	w.problem = container.NewVBox()
	w.notice = widget.NewLabel("")
	w.notice.Hide()
	objects := make([]fyne.CanvasObject, 0, len(w.entries))
	for _, e := range w.entries {
		objects = append(objects, e.panel.object())
	}
	w.stack = container.NewStack(objects...)
	w.list = widget.NewList(
		func() int { return len(w.entries) },
		func() fyne.CanvasObject { return widget.NewLabel("Telegram account") },
		func(id widget.ListItemID, item fyne.CanvasObject) {
			item.(*widget.Label).SetText(w.entries[id].title)
		},
	)
	w.list.OnSelected = func(id widget.ListItemID) {
		if w.isSyncing() {
			return
		}
		w.showStep(w.entries[id].step, false)
	}
	body := container.NewBorder(container.NewVBox(w.problem, w.notice), nil, nil, nil, w.stack)
	split := container.NewHSplit(w.list, body)
	split.SetOffset(0.22)
	w.root = split
	w.showStep(stepConnection, true)
}

func (w *view) isSyncing() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncing
}

func (w *view) setSyncing(on bool) {
	w.mu.Lock()
	w.syncing = on
	w.mu.Unlock()
}

func (w *view) setContext(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
}

func (w *view) context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctx
}

func (w *view) selectStep(step string) {
	if step == "" {
		return
	}
	for _, e := range w.entries {
		if e.step == step {
			w.showStep(step, true)
			return
		}
	}
}

func (w *view) showStep(step string, syncList bool) {
	w.mu.Lock()
	w.selected = step
	w.mu.Unlock()
	for _, e := range w.entries {
		if e.step == step {
			e.panel.object().Show()
			continue
		}
		e.panel.object().Hide()
	}
	if syncList {
		w.syncList(step)
	}
}

func (w *view) syncList(step string) {
	w.setSyncing(true)
	defer w.setSyncing(false)
	for i, e := range w.entries {
		if e.step == step {
			w.list.Select(i)
			return
		}
	}
}

func (w *view) selectedStep() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.selected
}

func (w *view) loadVisible(ctx context.Context) {
	w.vm.Refresh(ctx)
	if w.selectedStep() == stepActivity {
		w.vm.RefreshActivity(ctx)
	}
}

func (w *view) render(s vm.State) {
	for _, e := range w.entries {
		e.panel.update(s)
	}
	w.renderProblem(s)
	w.renderNotice(s)
}

func (w *view) window() fyne.Window {
	if w.win != nil {
		return w.win
	}
	app := fyne.CurrentApp()
	if app == nil {
		return nil
	}
	canvas := app.Driver().CanvasForObject(w.root)
	for _, win := range app.Driver().AllWindows() {
		if win.Canvas() == canvas {
			return win
		}
	}
	if windows := app.Driver().AllWindows(); len(windows) > 0 {
		return windows[0]
	}
	return nil
}

func (w *view) run(fn func(ctx context.Context)) {
	ctx := w.context()
	go fn(ctx)
}

func (w *view) renderNotice(s vm.State) {
	if s.Notice == "" {
		w.noticeKey = ""
		w.notice.Hide()
		return
	}
	if w.noticeKey == s.Notice {
		return
	}
	w.noticeKey = s.Notice
	if w.notifier != nil {
		w.notifier.Info(s.Notice)
		return
	}
	w.notice.SetText(s.Notice)
	w.notice.Show()
}

func (w *view) renderProblem(s vm.State) {
	if s.Problem == nil {
		w.clearProblem()
		return
	}
	p := *s.Problem
	w.problem.Objects = []fyne.CanvasObject{problemStrip(p)}
	w.problem.Refresh()
	w.deliver(p)
}

func (w *view) clearProblem() {
	w.problem.Objects = nil
	w.problem.Refresh()
	wasBanner := w.bannerShown
	w.bannerShown, w.shownKey, w.routedKey = false, "", ""
	if wasBanner && w.notifier != nil {
		w.notifier.ClearBanner()
	}
}

func (w *view) deliver(p model.Problem) {
	key := string(p.Code) + "/" + p.RequestID + "/" + p.Title
	switch p.Presentation {
	case model.PresentRoute:
		w.deliverRoute(p, key)
	case model.PresentBanner:
		if w.notifier != nil {
			w.bannerShown = true
			w.notifier.Problem(p)
		}
	case model.PresentDialog:
		if w.notifier != nil && w.shownKey != key {
			w.shownKey = key
			w.notifier.Problem(p)
		}
	}
}

func (w *view) deliverRoute(p model.Problem, key string) {
	if w.routedKey == key {
		return
	}
	w.routedKey = key
	if w.notifier != nil {
		w.notifier.Problem(p)
		return
	}
	if p.Route != "" && w.nav != nil {
		w.nav.Navigate(p.Route, nil)
	}
}

func problemStrip(p model.Problem) fyne.CanvasObject {
	rows := []fyne.CanvasObject{widget.NewLabelWithStyle(p.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})}
	if p.Body != "" {
		rows = append(rows, newWrapped(p.Body))
	}
	icon := widget.NewIcon(theme.NewWarningThemedResource(theme.WarningIcon()))
	return container.NewBorder(nil, nil, icon, nil, container.NewVBox(rows...))
}

func newWrapped(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func heading(text string) fyne.CanvasObject {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func fill(box *fyne.Container, rows []fyne.CanvasObject) {
	box.Objects = rows
	box.Refresh()
}

func pairRow(name, value string) fyne.CanvasObject {
	label := widget.NewLabel(value)
	label.Selectable = true
	label.Wrapping = fyne.TextWrapBreak
	return container.NewBorder(nil, nil, widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, label)
}

func scrolled(content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVScroll(content)
}
