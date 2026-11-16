package connect

import (
	"context"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	claimTitle        = "Set up this server"
	claimHelp         = "Paste the setup token the server logged when it started. It works once."
	plainHTTPWarning  = "This connection uses plain http through a local tunnel. The certificate is not checked."
	pairTitle         = "Pair this device"
	savedMessagesText = "Send a code to my Telegram Saved Messages"
	savedMessagesOff  = "Telegram is not linked yet. Use the host command instead."
	hostChoiceText    = "I will run butterdbd admin-token on the server"
	codePlaceholder   = "XXXXX-XXXXX"
	pairLabel         = "Pair"
	claimLabel        = "Claim"
)

type claimPanel struct {
	w       *view
	box     *fyne.Container
	token   *widget.Entry
	device  *widget.Entry
	warning *widget.Label
	claim   *actionButton
	cancel  *actionButton
}

func newClaimPanel(w *view, defaultName string) *claimPanel {
	p := &claimPanel{w: w}
	p.token = widget.NewPasswordEntry()
	p.token.SetPlaceHolder("bdst1_…")
	p.token.OnChanged = w.vm.SetSetupToken
	p.token.OnSubmitted = func(string) { p.submit() }
	p.device = widget.NewEntry()
	p.device.SetText(defaultName)
	p.device.OnSubmitted = func(string) { p.submit() }
	p.warning = newWrappedLabel(plainHTTPWarning)
	p.claim = newActionButton(claimLabel, theme.ConfirmIcon(), p.submit)
	p.claim.Importance = widget.HighImportance
	p.cancel = newActionButton("Cancel", theme.CancelIcon(), w.vm.Cancel)
	p.cancel.onEscape = w.vm.Cancel
	p.claim.onEscape = w.vm.Cancel
	form := widget.NewForm(
		widget.NewFormItem("Setup token", p.token),
		widget.NewFormItem("Device name", p.device),
	)
	p.box = container.NewVBox(
		newHeading(claimTitle),
		newWrappedLabel(claimHelp),
		p.warning,
		form,
		container.NewHBox(p.cancel, p.claim),
	)
	return p
}

func (p *claimPanel) object() fyne.CanvasObject { return p.box }

func (p *claimPanel) focusFirst() { focusOn(p.token) }

func (p *claimPanel) focusField(field string) bool {
	switch field {
	case vm.FieldSetupToken:
		focusOn(p.token)
	case vm.FieldDeviceName:
		focusOn(p.device)
	default:
		return false
	}
	return true
}

func (p *claimPanel) submit() {
	name := p.device.Text
	p.w.run(func(ctx context.Context) { p.w.vm.Claim(ctx, name) })
}

func (p *claimPanel) update(s vm.State) {
	if p.w.rendered.setupToken != "" && s.SetupToken == "" && p.token.Text != "" {
		p.token.SetText("")
	}
	setVisible(p.warning, s.PlainHTTP)
	if s.Loading {
		p.claim.Disable()
		return
	}
	p.claim.Enable()
}

type pairPanel struct {
	w         *view
	box       *fyne.Container
	device    *widget.Entry
	code      *widget.Entry
	request   *actionButton
	offNotice *widget.Label
	sent      *widget.Label
	warning   *widget.Label
	pair      *actionButton
	cancel    *actionButton
	countdown countdownState
}

type countdownState struct {
	expires time.Time
	stop    chan struct{}
}

func newPairPanel(w *view, defaultName string) *pairPanel {
	p := &pairPanel{w: w}
	p.device = widget.NewEntry()
	p.device.SetText(defaultName)
	p.code = widget.NewEntry()
	p.code.SetPlaceHolder(codePlaceholder)
	p.code.OnChanged = p.onCodeChanged
	p.code.OnSubmitted = func(string) { p.submit() }
	p.request = newActionButton(savedMessagesText, theme.MailSendIcon(), p.onRequest)
	p.offNotice = newWrappedLabel(savedMessagesOff)
	p.sent = newWrappedLabel("")
	p.warning = newWrappedLabel(plainHTTPWarning)
	p.pair = newActionButton(pairLabel, theme.ConfirmIcon(), p.submit)
	p.pair.Importance = widget.HighImportance
	p.pair.Disable()
	p.cancel = newActionButton("Cancel", theme.CancelIcon(), w.vm.Cancel)
	p.cancel.onEscape = w.vm.Cancel
	p.pair.onEscape = w.vm.Cancel
	hostBox := container.NewVBox(
		newWrappedLabel(hostChoiceText),
		shell.NewCopyField("Command", vm.HostCommand, w.clip),
	)
	p.box = container.NewVBox(
		newHeading(pairTitle),
		p.warning,
		widget.NewForm(widget.NewFormItem("Device name", p.device)),
		p.request,
		p.offNotice,
		p.sent,
		hostBox,
		widget.NewForm(widget.NewFormItem("Pairing code", p.code)),
		container.NewHBox(p.cancel, p.pair),
	)
	return p
}

func (p *pairPanel) object() fyne.CanvasObject { return p.box }

func (p *pairPanel) focusFirst() { focusOn(p.code) }

func (p *pairPanel) focusField(field string) bool {
	switch field {
	case vm.FieldPairCode:
		focusOn(p.code)
	case vm.FieldDeviceName:
		focusOn(p.device)
	default:
		return false
	}
	return true
}

func (p *pairPanel) onCodeChanged(text string) {
	formatted := p.w.vm.SetPairingCode(text)
	if formatted != text {
		p.code.SetText(formatted)
		p.code.CursorColumn = len(formatted)
		p.code.Refresh()
	}
}

func (p *pairPanel) onRequest() {
	name := p.device.Text
	p.w.run(func(ctx context.Context) { p.w.vm.RequestPairing(ctx, name) })
}

func (p *pairPanel) submit() {
	name := p.device.Text
	p.w.run(func(ctx context.Context) { p.w.vm.CompletePairing(ctx, name) })
}

func (p *pairPanel) update(s vm.State) {
	if p.w.rendered.pairingCode != "" && s.PairingCode == "" && p.code.Text != "" {
		p.code.SetText("")
	}
	setVisible(p.warning, s.PlainHTTP)
	setVisible(p.offNotice, !s.SavedMessagesEnabled)
	if s.SavedMessagesEnabled && !s.Loading {
		p.request.Enable()
	} else {
		p.request.Disable()
	}
	if s.PairingCodeComplete && !s.Loading {
		p.pair.Enable()
	} else {
		p.pair.Disable()
	}
	p.updateCountdown(s)
}

func (p *pairPanel) updateCountdown(s vm.State) {
	if !s.PairingRequested || s.PairingExpiresAt.IsZero() {
		p.stopCountdown()
		p.sent.SetText("")
		p.sent.Hide()
		return
	}
	if p.countdown.stop != nil && p.countdown.expires.Equal(s.PairingExpiresAt) {
		return
	}
	p.stopCountdown()
	p.sent.Show()
	p.startCountdown(s.PairingExpiresAt)
}

func (p *pairPanel) startCountdown(expires time.Time) {
	stop := make(chan struct{})
	p.countdown = countdownState{expires: expires, stop: stop}
	p.sent.SetText(sentText(expires, p.w.now()))
	go p.tick(expires, stop)
}

func (p *pairPanel) tick(expires time.Time, stop chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		now := p.w.now()
		p.w.dispatch(func() { p.sent.SetText(sentText(expires, now)) })
		if !now.Before(expires) {
			return
		}
	}
}

func (p *pairPanel) stopCountdown() {
	if p.countdown.stop != nil {
		close(p.countdown.stop)
	}
	p.countdown = countdownState{}
}

func sentText(expires, now time.Time) string {
	remaining := expires.Sub(now)
	if remaining <= 0 {
		return "The code expired. Request a new one."
	}
	seconds := int(remaining.Round(time.Second) / time.Second)
	return "Code sent. It expires in " + strconv.Itoa(seconds/60) + ":" + twoDigits(seconds%60) + "."
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
