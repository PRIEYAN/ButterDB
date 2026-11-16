package settings

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type aboutPanel struct {
	root fyne.CanvasObject
	body *fyne.Container
}

func newAboutPanel(*view) *aboutPanel {
	p := &aboutPanel{body: container.NewVBox()}
	p.root = scrolledCard(stepAbout, "About", p.body)
	return p
}

func (p *aboutPanel) object() fyne.CanvasObject { return p.root }

func (p *aboutPanel) update(s vm.State) {
	a := s.About
	rows := []fyne.CanvasObject{
		monoPairRow("App version", a.AppVersion),
		monoPairRow("Go version", a.GoVersion),
		monoPairRow("Fyne version", a.FyneVersion),
		monoPairRow("Server version", orDash(a.ServerVersion)),
		shell.NewSectionGap(),
		shell.NewSectionTitle("Licences"),
	}
	for _, l := range a.Licences {
		rows = append(rows, container.NewBorder(nil, nil, nameCell(l.Name), nil, container.NewHBox(shell.NewValueBadge(l.Licence))))
	}
	fill(p.body, rows)
}
