package objects

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const screenTitle = "Objects"

type Screen struct {
	browser *browser
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator, w fyne.Window, opts Options) *Screen {
	return &Screen{browser: newBrowser(v, nav, w, opts)}
}

func NewView(v *vm.ViewModel, nav shell.Navigator, w fyne.Window) fyne.CanvasObject {
	b := newBrowser(v, nav, w, Options{})
	b.bindShortcuts()
	return b.root
}

func (s *Screen) Route() model.Route { return model.RouteObjects }

func (s *Screen) Title() string { return screenTitle }

func (s *Screen) Icon() fyne.Resource { return theme.StorageIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return fyne.Key4 }

func (s *Screen) Content() fyne.CanvasObject { return s.browser.root }

func (s *Screen) Show(ctx context.Context, p model.Params) { s.browser.show(ctx, p) }

func (s *Screen) Hide() { s.browser.hide() }

var _ shell.Screen = (*Screen)(nil)
