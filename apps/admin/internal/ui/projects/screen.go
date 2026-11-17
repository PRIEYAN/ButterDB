package projects

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/projects/vm"
)

type Screen struct {
	vm   *vm.ViewModel
	view *view
}

func NewScreen(v *vm.ViewModel, n shell.Notifier, onOpened func(vm.Project)) *Screen {
	return &Screen{vm: v, view: newView(v, n, onOpened)}
}

func (s *Screen) Route() model.Route { return model.RouteProjects }

func (s *Screen) Title() string { return "Projects" }

func (s *Screen) Icon() fyne.Resource { return theme.FolderIcon() }

func (s *Screen) Shortcut() fyne.KeyName { return "" }

func (s *Screen) Content() fyne.CanvasObject { return s.view.root }

func (s *Screen) Show(ctx context.Context, _ model.Params) {
	s.view.setContext(ctx)
	go s.vm.Enter(ctx)
}

func (s *Screen) Hide() {
	s.view.clearSecrets()
}
