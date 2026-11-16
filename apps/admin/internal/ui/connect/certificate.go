package connect

import (
	"context"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	verifyTitle        = "Verify this server"
	verifyInstructions = "On the server, run: journalctl -u butterdbd | grep \"admin listener\" (or docker logs <container>) and compare with tls_fingerprint_display."
	rebindNotice       = "This server now presents a different key than the one you trusted. Compare it with the server log before you continue."
	matchLabel         = "Fingerprints match"
	trustSystemLabel   = "Trust system certificate"
	pinInsteadLabel    = "Pin the key instead (advanced)"
	pinKeyLabel        = "Pin this key"
	dateLayout         = "2006-01-02"
)

type certificatePanel struct {
	w            *view
	card         *shell.WindowCard
	rebind       *widget.Label
	fingerprint  *widget.Label
	copyPrint    fyne.CanvasObject
	instructions *widget.Label
	facts        *widget.Label
	pinInstead   *widget.Check
	confirm      *actionButton
	cancel       *actionButton
	pinned       bool
	text         string
}

func newCertificatePanel(w *view) *certificatePanel {
	p := &certificatePanel{w: w}
	p.rebind = newWrappedLabel(rebindNotice)
	p.fingerprint = newMonospaceLabel("")
	p.instructions = newWrappedLabel(verifyInstructions)
	p.facts = newWrappedLabel("")
	p.pinInstead = widget.NewCheck(pinInsteadLabel, func(bool) { p.refreshConfirmLabel() })
	p.confirm = newActionButton(matchLabel, theme.ConfirmIcon(), p.onConfirm)
	p.cancel = newActionButton("Cancel", theme.CancelIcon(), w.vm.Cancel)
	p.cancel.Importance = widget.HighImportance
	p.confirm.onEscape = w.vm.Cancel
	p.cancel.onEscape = w.vm.Cancel
	p.copyPrint = widget.NewButtonWithIcon("Copy fingerprint", theme.ContentCopyIcon(), func() { w.clip.SetText(p.text) })
	p.facts.TextStyle = fyne.TextStyle{Monospace: true}
	p.card = panelCard("connect/verify", verifyTitle,
		shell.NewCallout(shell.CalloutWarning, p.rebind),
		shell.NewCodeBlock(p.fingerprint),
		container.NewHBox(shell.Action(p.copyPrint)),
		shell.NewCallout(shell.CalloutInfo, p.instructions),
		p.facts,
		p.pinInstead,
		shell.NewSectionGap(),
		shell.Actions(p.cancel, p.confirm),
	)
	return p
}

func (p *certificatePanel) object() fyne.CanvasObject { return p.card }

func (p *certificatePanel) focusFirst() { focusOn(p.cancel) }

func (p *certificatePanel) focusField(string) bool { return false }

func (p *certificatePanel) onConfirm() {
	pin := !p.systemVerified() || p.pinInstead.Checked
	p.w.run(func(ctx context.Context) { p.w.vm.ConfirmCertificate(ctx, pin) })
}

func (p *certificatePanel) systemVerified() bool {
	return !p.pinned
}

func (p *certificatePanel) update(s vm.State) {
	cert := s.Certificate
	if cert == nil {
		return
	}
	p.pinned = !cert.VerifiesWithSystemRoots
	p.text = cert.Fingerprint
	p.fingerprint.SetText(cert.Fingerprint)
	p.facts.SetText(certificateFacts(cert))
	setVisible(p.rebind, s.Rebinding)
	setVisible(p.instructions, p.pinned)
	setVisible(p.pinInstead, !p.pinned)
	p.refreshConfirmLabel()
	if s.Loading {
		p.confirm.Disable()
	} else {
		p.confirm.Enable()
	}
}

func (p *certificatePanel) refreshConfirmLabel() {
	switch {
	case p.pinned:
		p.confirm.SetText(matchLabel)
	case p.pinInstead.Checked:
		p.confirm.SetText(pinKeyLabel)
	default:
		p.confirm.SetText(trustSystemLabel)
	}
}

func certificateFacts(cert *vm.CertificateView) string {
	lines := []string{"Subject: " + cert.Subject}
	if len(cert.DNSNames) > 0 {
		lines = append(lines, "DNS names: "+strings.Join(cert.DNSNames, ", "))
	}
	if len(cert.IPAddresses) > 0 {
		lines = append(lines, "IP addresses: "+strings.Join(cert.IPAddresses, ", "))
	}
	if !cert.NotAfter.IsZero() {
		lines = append(lines, "Expires: "+cert.NotAfter.Format(dateLayout))
	}
	return strings.Join(lines, "\n")
}

func setVisible(object fyne.CanvasObject, visible bool) {
	if visible {
		object.Show()
		return
	}
	object.Hide()
}
