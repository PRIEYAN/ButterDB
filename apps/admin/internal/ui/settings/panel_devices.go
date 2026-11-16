package settings

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

const deviceColumns = 7

type devicesPanel struct {
	w    *view
	root fyne.CanvasObject
	rows *fyne.Container
}

func newDevicesPanel(w *view) *devicesPanel {
	p := &devicesPanel{w: w, rows: container.NewVBox()}
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		w.run(func(ctx context.Context) { w.vm.RefreshDevices(ctx) })
	})
	p.root = container.NewBorder(container.NewBorder(nil, nil, heading("Devices"), refresh), nil, nil, nil, scrolled(p.rows))
	return p
}

func (p *devicesPanel) object() fyne.CanvasObject { return p.root }

func (p *devicesPanel) update(s vm.State) {
	now := p.w.vm.Now()
	grid := container.NewGridWithColumns(deviceColumns, headerCells()...)
	for _, row := range s.Devices.Rows {
		for _, text := range deviceRowText(row, now) {
			grid.Add(cell(text))
		}
		grid.Add(p.revokeCell(row))
	}
	fill(p.rows, []fyne.CanvasObject{grid})
}

func headerCells() []fyne.CanvasObject {
	names := []string{"Name", "Platform", "Created via", "Last seen", "Last IP", "Status", ""}
	cells := make([]fyne.CanvasObject, len(names))
	for i, name := range names {
		cells[i] = widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	return cells
}

func cell(text string) fyne.CanvasObject {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapBreak
	label.Selectable = true
	return label
}

func (p *devicesPanel) revokeCell(row vm.DeviceRow) fyne.CanvasObject {
	if row.Revoked {
		return widget.NewLabel("")
	}
	id := row.Device.DeviceID
	name := row.Device.Name
	button := widget.NewButtonWithIcon("Revoke…", theme.DeleteIcon(), func() { p.confirmRevoke(id, name, row.This) })
	button.Importance = widget.DangerImportance
	return button
}

func (p *devicesPanel) confirmRevoke(id, name string, this bool) {
	win := p.w.window()
	if win == nil {
		return
	}
	body := "Revoke " + name + "? It can no longer use this server until it is paired again."
	if this {
		body = "Revoke this device? You will be signed out of this server on this computer."
	}
	shell.ConfirmDestructive(win, "Revoke device", body, "Revoke", func() { p.revoke(id, false) })
}

func (p *devicesPanel) revoke(id string, confirmLast bool) {
	p.w.run(func(ctx context.Context) {
		outcome := p.w.vm.Revoke(ctx, id, confirmLast)
		p.w.post(func() { p.afterRevoke(id, outcome) })
	})
}

func (p *devicesPanel) afterRevoke(id string, outcome vm.RevokeOutcome) {
	switch outcome {
	case vm.RevokeNeedsLastConfirmation:
		p.confirmLastDevice(id)
	case vm.RevokedThisDevice:
		p.w.nav.Navigate(outcome.Route(), nil)
	}
}

func (p *devicesPanel) confirmLastDevice(id string) {
	win := p.w.window()
	if win == nil {
		return
	}
	shell.ConfirmDestructive(win, lastDeviceTitle, lastDeviceBody, "Revoke last device", func() { p.revoke(id, true) })
}
