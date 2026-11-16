package dashboard

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
)

type Screen struct {
	vm   *vm.ViewModel
	view *view
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator, n shell.Notifier, clip model.Clipboard) *Screen {
	return &Screen{vm: v, view: newView(v, nav, n, clip)}
}

func NewView(v *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject {
	return newView(v, nav, nil, appClipboard{}).root
}

func (s *Screen) Route() model.Route { return model.RouteDashboard }

func (s *Screen) Title() string { return "Dashboard" }

func (s *Screen) Icon() fyne.Resource { return theme.HomeIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return fyne.Key1 }

func (s *Screen) Content() fyne.CanvasObject { return s.view.root }

func (s *Screen) Show(ctx context.Context, p model.Params) {
	s.view.setContext(ctx)
	s.vm.Start(ctx)
}

func (s *Screen) Hide() {
	s.vm.Stop()
}

type appClipboard struct{}

func (appClipboard) SetText(text string) { fyne.CurrentApp().Clipboard().SetContent(text) }

func (appClipboard) Text() string { return fyne.CurrentApp().Clipboard().Content() }
