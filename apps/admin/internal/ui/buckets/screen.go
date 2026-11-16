package buckets

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
)

const screenTitle = "Buckets"

type Env struct {
	Window    func() fyne.Window
	Clipboard model.Clipboard
	Spawn     func(func())
}

type Screen struct {
	vm   *vm.ViewModel
	nav  shell.Navigator
	env  Env
	root fyne.CanvasObject

	list     *bucketList
	strip    *problemStrip
	progress *widget.ProgressBarInfinite
	empty    *widget.Label
	toolbar  toolbar

	ctx           context.Context
	shownNotEmpty *vm.NotEmpty
	routed        *model.Problem
	newShortcut   *desktop.CustomShortcut
	cancelWatch   func()
}

func NewView(v *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject {
	return NewScreen(v, nav).Content()
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator) *Screen {
	return NewScreenWithEnv(v, nav, Env{})
}

func NewScreenWithEnv(v *vm.ViewModel, nav shell.Navigator, env Env) *Screen {
	if v == nil {
		panic("buckets: nil view model")
	}
	if nav == nil {
		panic("buckets: nil navigator")
	}
	s := &Screen{vm: v, nav: nav, env: env, ctx: context.Background()}
	s.build()
	s.cancelWatch = v.State().Subscribe(func(st vm.State) { fyne.Do(func() { s.render(st) }) })
	s.render(v.State().Get())
	return s
}

func (s *Screen) Route() model.Route { return model.RouteBuckets }

func (s *Screen) Title() string { return screenTitle }

func (s *Screen) Icon() fyne.Resource { return theme.StorageIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return fyne.Key2 }

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
	s.strip = newProblemStrip(s.clipboard(), s.retry, s.vm.DismissProblem)
	s.progress = widget.NewProgressBarInfinite()
	s.empty = widget.NewLabel("No buckets yet. Choose New bucket to create one.")
	s.list = newBucketList(s.vm.Now, s.vm.Sort, s.updateToolbar, s.openSelected)
	s.toolbar = newToolbar(toolbarActions{
		create:   s.openCreate,
		settings: s.openSettings,
		browse:   s.openSelected,
		remove:   s.confirmDelete,
		refresh:  s.retry,
	})
	top := container.NewVBox(s.toolbar.object, s.strip.object, s.progress)
	body := container.NewStack(s.list.table, container.NewCenter(s.empty))
	s.root = container.NewBorder(top, nil, nil, nil, body)
}

func (s *Screen) retry() {
	ctx := s.ctx
	s.spawn(func() { s.vm.Refresh(ctx) })
}

func (s *Screen) render(st vm.State) {
	s.list.setBuckets(st.Buckets, st.SortColumn, st.SortDescending)
	s.strip.show(st.Problem)
	s.routeProblem(st.Problem)
	s.setLoading(st.Loading || st.Working)
	s.empty.Hidden = len(st.Buckets) > 0 || st.Loading
	s.empty.Refresh()
	s.showNotEmptyOnce(st.NotEmpty)
	s.updateToolbar()
}

func (s *Screen) updateToolbar() {
	s.toolbar.setSelection(s.list.selected != "")
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

func (s *Screen) clipboard() model.Clipboard {
	if s.env.Clipboard != nil {
		return s.env.Clipboard
	}
	return appClipboard{}
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
