package settings

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type aboutPanel struct {
	root fyne.CanvasObject
	body *fyne.Container
}

func newAboutPanel(*view) *aboutPanel {
	p := &aboutPanel{body: container.NewVBox()}
	p.root = scrolled(container.NewVBox(heading("About"), p.body))
	return p
}

func (p *aboutPanel) object() fyne.CanvasObject { return p.root }

func (p *aboutPanel) update(s vm.State) {
	a := s.About
	rows := []fyne.CanvasObject{
		pairRow("App version", a.AppVersion),
		pairRow("Go version", a.GoVersion),
		pairRow("Fyne version", a.FyneVersion),
		pairRow("Server version", orDash(a.ServerVersion)),
		heading("Licences"),
	}
	for _, l := range a.Licences {
		rows = append(rows, pairRow(l.Name, l.Licence))
	}
	fill(p.body, rows)
}
