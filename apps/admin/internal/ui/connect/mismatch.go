package connect

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	mismatchTitle  = "Server identity changed"
	trustNewLabel  = "Trust the new key…"
	disconnectText = "Disconnect"
)

type mismatchPanel struct {
	w          *view
	box        *fyne.Container
	body       *fyne.Container
	trustNew   *actionButton
	disconnect *actionButton
}

func newMismatchPanel(w *view) *mismatchPanel {
	p := &mismatchPanel{w: w}
	p.body = container.NewVBox()
	p.disconnect = newActionButton(disconnectText, theme.LogoutIcon(), w.vm.Disconnect)
	p.disconnect.Importance = widget.HighImportance
	p.trustNew = newActionButton(trustNewLabel, theme.WarningIcon(), func() {
		w.run(func(ctx context.Context) { w.vm.TrustNewKey(ctx) })
	})
	p.disconnect.onEscape = w.vm.Disconnect
	p.trustNew.onEscape = w.vm.Disconnect
	p.box = container.NewVBox(
		newHeading(mismatchTitle),
		p.body,
		container.NewHBox(p.disconnect, p.trustNew),
	)
	return p
}

func (p *mismatchPanel) object() fyne.CanvasObject { return p.box }

func (p *mismatchPanel) focusFirst() { focusOn(p.disconnect) }

func (p *mismatchPanel) focusField(string) bool { return false }

func (p *mismatchPanel) update(s vm.State) {
	m := s.Mismatch
	if m == nil {
		return
	}
	p.body.RemoveAll()
	p.body.Add(newWrappedLabel("The certificate key of " + m.ServerLabel + " is not the one this device trusted. This happens when the server's tls/admin.key was deleted or replaced, or when someone is intercepting the connection."))
	p.body.Add(shell.NewCopyField("Expected", m.Expected, p.w.clip))
	p.body.Add(shell.NewCopyField("Received", m.Received, p.w.clip))
	p.body.Refresh()
	if s.Loading {
		p.trustNew.Disable()
	} else {
		p.trustNew.Enable()
	}
}
