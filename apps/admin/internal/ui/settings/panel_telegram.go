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

type telegramPanel struct {
	w      *view
	root   fyne.CanvasObject
	facts  *fyne.Container
	logout *widget.Button
	remove *widget.Button
}

func newTelegramPanel(w *view) *telegramPanel {
	p := &telegramPanel{w: w, facts: container.NewVBox()}
	p.logout = widget.NewButtonWithIcon("Log out of Telegram", theme.LogoutIcon(), p.logoutNow)
	p.remove = widget.NewButtonWithIcon("Remove this account from this device…", theme.DeleteIcon(), p.confirmRemove)
	p.remove.Importance = widget.DangerImportance
	p.root = scrolledCard(stepTelegram, "Telegram account", container.NewVBox(
		p.facts,
		shell.NewSectionGap(),
		shell.NewCallout(shell.CalloutWarning, container.NewVBox(heading("Account safety"), newWrapped(safetyNotice))),
		shell.NewSectionGap(),
		shell.Actions(p.logout, p.remove),
	))
	return p
}

func (p *telegramPanel) object() fyne.CanvasObject { return p.root }

func (p *telegramPanel) update(s vm.State) {
	fill(p.facts, p.rows(s.Telegram))
}

func (p *telegramPanel) rows(t vm.TelegramState) []fyne.CanvasObject {
	if !t.Loaded {
		return []fyne.CanvasObject{widget.NewLabel("Waiting for data…")}
	}
	if !t.Linked() {
		return []fyne.CanvasObject{widget.NewLabel("No Telegram account is logged in.")}
	}
	a := t.Status.Account
	rows := []fyne.CanvasObject{pairRow("Name", orDash(a.FirstName))}
	if a.Username != "" {
		rows = append(rows, pairRow("Username", "@"+a.Username))
	}
	rows = append(rows, monoPairRow("Phone", orDash(a.PhoneMasked)))
	rows = append(rows, monoPairRow("Logged in", timeWithAge(a.LinkedAt, p.w.vm.Now())))
	if a.Connected {
		return append(rows, pairRow("Connection", "Connected"))
	}
	return append(rows, pairRow("Connection", "Disconnected"))
}

func (p *telegramPanel) logoutNow() {
	p.leave(p.w.vm.Logout)
}

func (p *telegramPanel) confirmRemove() {
	win := p.w.window()
	if win == nil {
		return
	}
	shell.ConfirmDestructive(win, removeDeviceTitle, removeDeviceBody, "Remove", func() { p.leave(p.w.vm.RemoveDevice) })
}

func (p *telegramPanel) leave(call func(ctx context.Context) vm.AccountOutcome) {
	p.w.run(func(ctx context.Context) {
		outcome := call(ctx)
		p.w.post(func() {
			if route := outcome.Route(); route != "" {
				p.w.nav.Navigate(route, nil)
			}
		})
	})
}
