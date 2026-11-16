package settings

import (
	"context"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type connectionPanel struct {
	w         *view
	root      fyne.CanvasObject
	label     *widget.Entry
	save      *widget.Button
	facts     *fyne.Container
	move      *widget.Button
	reverify  *widget.Button
	verdict   *fyne.Container
	lastLabel string
}

func newConnectionPanel(w *view) *connectionPanel {
	p := &connectionPanel{w: w}
	p.label = widget.NewEntry()
	p.label.SetPlaceHolder("Server label")
	p.save = widget.NewButtonWithIcon("Save label", theme.DocumentSaveIcon(), p.onSave)
	p.label.OnSubmitted = func(string) { p.onSave() }
	p.facts = container.NewVBox()
	p.verdict = container.NewVBox()
	p.move = widget.NewButtonWithIcon("Move to keychain", theme.UploadIcon(), p.onMove)
	p.reverify = widget.NewButtonWithIcon("Re-verify certificate", theme.ViewRefreshIcon(), p.onReverify)
	switchServer := widget.NewButtonWithIcon("Switch server", theme.LogoutIcon(), func() {
		w.nav.Navigate(model.RouteConnect, nil)
	})
	labelRow := container.NewBorder(nil, nil, widget.NewLabelWithStyle("Label", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), p.save, p.label)
	p.root = scrolled(container.NewVBox(
		heading("Connection"),
		labelRow,
		p.facts,
		container.NewHBox(p.move, p.reverify, switchServer),
		p.verdict,
	))
	return p
}

func (p *connectionPanel) object() fyne.CanvasObject { return p.root }

func (p *connectionPanel) onSave() {
	text := p.label.Text
	go p.w.vm.RenameServer(text)
}

func (p *connectionPanel) onMove() {
	go p.w.vm.MoveToKeychain()
}

func (p *connectionPanel) onReverify() {
	p.w.run(func(ctx context.Context) { p.w.vm.Reverify(ctx) })
}

func (p *connectionPanel) update(s vm.State) {
	c := s.Connection
	if p.lastLabel != c.Profile.Label {
		p.lastLabel = c.Profile.Label
		p.label.SetText(c.Profile.Label)
	}
	fill(p.facts, p.factRows(c))
	if c.CanMoveToKeychain {
		p.move.Show()
	} else {
		p.move.Hide()
	}
	fill(p.verdict, p.verdictRows(c.Reverify))
}

func (p *connectionPanel) factRows(c vm.ConnectionState) []fyne.CanvasObject {
	rows := []fyne.CanvasObject{
		shell.NewCopyField("URL", c.Profile.BaseURL, p.w.clip),
		shell.NewCopyField("Server ID", c.Profile.ServerID, p.w.clip),
		pairRow("Server version", orDash(c.ServerVersion)),
		pairRow("Server state", orDash(c.ServerState)),
		pairRow("Trust", trustText(c.Profile.Trust)),
	}
	if c.FingerprintDisplay != "" {
		rows = append(rows, shell.NewCopyField("Pinned fingerprint", c.FingerprintDisplay, p.w.clip))
	}
	return append(rows, pairRow("Token storage", storageText(c.TokenStorage)))
}

func (p *connectionPanel) verdictRows(r vm.ReverifyResult) []fyne.CanvasObject {
	switch r.Status {
	case vm.ReverifyMatch:
		return []fyne.CanvasObject{iconText(theme.NewSuccessThemedResource(theme.ConfirmIcon()), "The certificate matches what this device trusts.")}
	case vm.ReverifyNotApplicable:
		return []fyne.CanvasObject{iconText(theme.InfoIcon(), "There is no certificate to check for this connection.")}
	case vm.ReverifyChanged:
		return changedRows(r)
	}
	return nil
}

func changedRows(r vm.ReverifyResult) []fyne.CanvasObject {
	rows := []fyne.CanvasObject{iconText(theme.NewErrorThemedResource(theme.ErrorIcon()), "The server presents a different certificate than this device trusts.")}
	if r.Expected != "" {
		rows = append(rows, pairRow("Trusted", strings.ReplaceAll(r.Expected, "\n", " ")))
	}
	return append(rows, pairRow("Presented", strings.ReplaceAll(r.Got, "\n", " ")))
}

func iconText(icon fyne.Resource, text string) fyne.CanvasObject {
	return container.NewBorder(nil, nil, widget.NewIcon(icon), nil, newWrapped(text))
}
