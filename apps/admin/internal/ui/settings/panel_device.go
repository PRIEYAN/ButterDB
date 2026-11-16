package settings

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type devicePanel struct {
	w        *view
	root     fyne.CanvasObject
	name     *widget.Entry
	save     *widget.Button
	facts    *fyne.Container
	rotate   *widget.Button
	lastName string
}

func newDevicePanel(w *view) *devicePanel {
	p := &devicePanel{w: w}
	p.name = widget.NewEntry()
	p.name.SetPlaceHolder("Device name")
	p.name.OnSubmitted = func(string) { p.onSave() }
	p.save = widget.NewButtonWithIcon("Save name", theme.DocumentSaveIcon(), p.onSave)
	p.facts = container.NewVBox()
	p.rotate = widget.NewButtonWithIcon("Rotate token now", theme.ViewRefreshIcon(), p.onRotate)
	nameRow := container.NewBorder(nil, nil, nameCell("Name"), shell.Action(p.save), p.name)
	p.root = scrolledCard(stepDevice, "This device", container.NewVBox(nameRow, p.facts, shell.NewSectionGap(), container.NewHBox(shell.Action(p.rotate))))
	return p
}

func (p *devicePanel) object() fyne.CanvasObject { return p.root }

func (p *devicePanel) onSave() {
	text := p.name.Text
	p.w.run(func(ctx context.Context) { p.w.vm.RenameDevice(ctx, text) })
}

func (p *devicePanel) onRotate() {
	p.w.run(func(ctx context.Context) { p.w.vm.RotateToken(ctx) })
}

func (p *devicePanel) update(s vm.State) {
	current := p.currentDevice(s)
	if p.lastName != current.Name {
		p.lastName = current.Name
		p.name.SetText(current.Name)
	}
	now := p.w.vm.Now()
	rows := []fyne.CanvasObject{
		pairRow("Created via", orDash(current.CreatedVia)),
		monoPairRow("Platform", orDash(current.Platform)),
		monoPairRow("Token issued", timeWithAge(tokenIssuedAt(s, current), now)),
	}
	fill(p.facts, rows)
}

func (p *devicePanel) currentDevice(s vm.State) adminv1.Device {
	if s.Device.Found {
		return s.Device.Current
	}
	return adminv1.Device{Name: s.Connection.Profile.DeviceName}
}

func tokenIssuedAt(s vm.State, current adminv1.Device) (issued time.Time) {
	if !s.Connection.Profile.TokenIssuedAt.IsZero() {
		return s.Connection.Profile.TokenIssuedAt
	}
	return current.TokenIssuedAt
}

func deviceRowText(row vm.DeviceRow, now time.Time) []string {
	d := row.Device
	name := d.Name
	if row.This {
		name = fmt.Sprintf("%s (This device)", name)
	}
	return []string{
		name,
		orDash(d.Platform),
		orDash(d.CreatedVia),
		shell.FormatAge(d.LastSeenAt, now),
		orDash(d.LastSeenIP),
		deviceStatusText(d, now),
	}
}
