package connect

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

type screen struct {
	view *view
}

func NewScreen(v *vm.ViewModel, nav shell.Navigator) shell.Screen {
	return &screen{view: newView(v, nav, appClipboard{}, timeNow, fyne.Do)}
}

func (s *screen) Route() model.Route { return model.RouteConnect }

func (s *screen) Title() string { return "Connect" }

func (s *screen) Icon() fyne.Resource { return theme.ComputerIcon() }

func (s *screen) Shortcut() fyne.KeyName { return "" }

func (s *screen) Content() fyne.CanvasObject { return s.view.root }

func (s *screen) Show(ctx context.Context, _ model.Params) {
	s.view.bind(ctx)
	go s.view.vm.Refresh(ctx)
}

func (s *screen) Hide() {
	s.view.bind(context.Background())
	s.view.stopTimers()
}
