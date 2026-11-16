package keys

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

const (
	screenTitle = "Access keys"
	pageKicker  = "admin / keys"
	listLabel   = "keys/list"
	gridGap     = 8
)

type Env struct {
	Window    func() fyne.Window
	Clipboard model.Clipboard
	Schedule  vm.Scheduler
	Spawn     func(func())
}

type Screen struct {
	vm   *vm.ViewModel
	nav  shell.Navigator
	env  Env
	clip *vm.ClipboardGuard
	root fyne.CanvasObject

	list     *keyList
	strip    *problemStrip
	progress *widget.ProgressBarInfinite
	empty    *widget.Label
	toolbar  toolbar

	ctx         context.Context
	form        *keyForm
	secret      *vm.Secret
	secretView  *secretDialog
	routed      *model.Problem
	newShortcut *desktop.CustomShortcut
	syncing     bool
}

func NewView(v *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject {
	return NewScreen(v, nav).Content()
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator) *Screen {
	return NewScreenWithEnv(v, nav, Env{})
}

func NewScreenWithEnv(v *vm.ViewModel, nav shell.Navigator, env Env) *Screen {
	if v == nil {
		panic("keys: nil view model")
	}
	if nav == nil {
		panic("keys: nil navigator")
	}
	s := &Screen{vm: v, nav: nav, env: env, ctx: context.Background()}
	s.clip = vm.NewClipboardGuard(s.baseClipboard(), s.scheduler(), vm.ClipboardClearAfter)
	s.build()
	v.State().Subscribe(func(st vm.State) { fyne.Do(func() { s.render(st) }) })
	s.render(v.State().Get())
	return s
}

func (s *Screen) Route() model.Route { return model.RouteKeys }

func (s *Screen) Title() string { return screenTitle }

func (s *Screen) Icon() fyne.Resource { return theme.AccountIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return fyne.Key3 }

func (s *Screen) Content() fyne.CanvasObject { return s.root }

func (s *Screen) Show(ctx context.Context, _ model.Params) {
	s.ctx = ctx
	s.bindNewShortcut()
	s.spawn(func() { s.vm.Refresh(ctx) })
}

func (s *Screen) Hide() {
	s.unbindNewShortcut()
}

func (s *Screen) build() {
	s.strip = newProblemStrip(s.clip, s.retry, s.vm.DismissProblem)
	s.progress = widget.NewProgressBarInfinite()
	s.empty = widget.NewLabel("No access keys yet. Choose New key to create one.")
	s.list = newKeyList(s.vm.Now, s.updateToolbar, s.openEdit, s.copySelectedID)
	s.toolbar = newToolbar(toolbarActions{
		create:      s.openCreate,
		edit:        s.openEdit,
		revoke:      s.confirmRevoke,
		copyID:      s.copySelectedID,
		refresh:     s.retry,
		showRevoked: s.setShowRevoked,
	})
	header := shell.NewPageHeader(pageKicker, shell.NewHeading(screenTitle))
	top := container.NewVBox(header, s.toolbar.object, shell.NewSectionGap(), s.strip.object, s.progress)
	body := shell.NewFlushWindowCard(listLabel, container.NewStack(s.list.table, container.NewCenter(s.empty)))
	s.root = container.NewBorder(top, nil, nil, nil, shell.Inset(0, gridGap, gridGap, 0, body))
}

func (s *Screen) retry() {
	ctx := s.ctx
	s.spawn(func() { s.vm.Refresh(ctx) })
}

func (s *Screen) setShowRevoked(include bool) {
	if s.syncing || include == s.vm.State().Get().IncludeRevoked {
		return
	}
	ctx := s.ctx
	s.spawn(func() { s.vm.SetIncludeRevoked(ctx, include) })
}

func (s *Screen) render(st vm.State) {
	s.list.setKeys(st.Keys)
	s.syncing = true
	s.toolbar.revoked.SetChecked(st.IncludeRevoked)
	s.syncing = false
	s.strip.show(st.Problem)
	s.routeProblem(st.Problem)
	s.setLoading(st.Loading || st.Working)
	s.empty.Hidden = len(st.Keys) > 0 || st.Loading
	s.empty.Refresh()
	s.updateToolbar()
	if s.form != nil {
		s.form.setBucketOptions(st.BucketNames, s.form.scope().Buckets)
	}
	s.showSecretOnce(st.Secret)
}

func (s *Screen) updateToolbar() {
	key, ok := s.list.selectedKey()
	s.toolbar.setSelection(ok, ok && key.Status != statusRevoked)
}

func (s *Screen) routeProblem(p *model.Problem) {
	if p == nil || p.Presentation != model.PresentRoute || p.Route == "" || p == s.routed {
		return
	}
	s.routed = p
	s.nav.Navigate(p.Route, nil)
}

func (s *Screen) setLoading(on bool) {
	if on {
		s.progress.Show()
		s.progress.Start()
		return
	}
	s.progress.Stop()
	s.progress.Hide()
}

func (s *Screen) spawn(f func()) {
	if s.env.Spawn != nil {
		s.env.Spawn(f)
		return
	}
	go f()
}

func (s *Screen) baseClipboard() model.Clipboard {
	if s.env.Clipboard != nil {
		return s.env.Clipboard
	}
	return appClipboard{}
}

func (s *Screen) scheduler() vm.Scheduler {
	if s.env.Schedule != nil {
		return s.env.Schedule
	}
	return scheduleOnUI
}

func (s *Screen) window() fyne.Window {
	if s.env.Window != nil {
		return s.env.Window()
	}
	return windowOf(s.root)
}

func (s *Screen) bindNewShortcut() {
	s.unbindNewShortcut()
	win := s.window()
	if win == nil {
		return
	}
	s.newShortcut = &desktop.CustomShortcut{KeyName: fyne.KeyN, Modifier: fyne.KeyModifierShortcutDefault}
	win.Canvas().AddShortcut(s.newShortcut, func(fyne.Shortcut) { s.openCreate() })
}

func (s *Screen) unbindNewShortcut() {
	if s.newShortcut == nil {
		return
	}
	if win := s.window(); win != nil {
		win.Canvas().RemoveShortcut(s.newShortcut)
	}
	s.newShortcut = nil
}
