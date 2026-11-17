package settings

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type Screen struct {
	vm   *vm.ViewModel
	view *view
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator, n shell.Notifier, clip model.Clipboard, win fyne.Window) *Screen {
	return &Screen{vm: v, view: newView(v, nav, n, clip, win)}
}

func NewView(v *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject {
	return newView(v, nav, nil, appClipboard{}, nil).root
}

func (s *Screen) Route() model.Route { return model.RouteSettings }

func (s *Screen) Title() string { return "Settings" }

func (s *Screen) Icon() fyne.Resource { return theme.SettingsIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return fyne.Key6 }

func (s *Screen) Content() fyne.CanvasObject { return s.view.root }

func (s *Screen) Show(ctx context.Context, p model.Params) {
	s.view.setContext(ctx)
	s.view.selectStep(p[model.ParamStep])
	go s.view.loadVisible(ctx)
}

func (s *Screen) Hide() {
	s.view.project.clearSecrets()
}

type appClipboard struct{}

func (appClipboard) SetText(text string) { fyne.CurrentApp().Clipboard().SetContent(text) }

func (appClipboard) Text() string { return fyne.CurrentApp().Clipboard().Content() }
