package settings

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type telegramPanel struct {
	w      *view
	root   fyne.CanvasObject
	facts  *fyne.Container
	unlink *widget.Button
}

func newTelegramPanel(w *view) *telegramPanel {
	p := &telegramPanel{w: w, facts: container.NewVBox()}
	p.unlink = widget.NewButtonWithIcon("Unlink…", theme.DeleteIcon(), p.confirmUnlink)
	p.unlink.Importance = widget.DangerImportance
	p.unlink.Disable()
	p.root = scrolled(container.NewVBox(
		heading("Telegram account"),
		p.facts,
		widget.NewSeparator(),
		heading("Account safety"),
		newWrapped(safetyNotice),
		p.unlink,
	))
	return p
}

func (p *telegramPanel) object() fyne.CanvasObject { return p.root }

func (p *telegramPanel) update(s vm.State) {
	t := s.Telegram
	fill(p.facts, p.rows(t))
	if t.Linked() {
		p.unlink.Enable()
		return
	}
	p.unlink.Disable()
}

func (p *telegramPanel) rows(t vm.TelegramState) []fyne.CanvasObject {
	if !t.Loaded {
		return []fyne.CanvasObject{widget.NewLabel("Waiting for data…")}
	}
	if !t.Linked() {
		return []fyne.CanvasObject{widget.NewLabel("No Telegram account is linked.")}
	}
	a := t.Status.Account
	rows := []fyne.CanvasObject{pairRow("Name", orDash(a.FirstName))}
	if a.Username != "" {
		rows = append(rows, pairRow("Username", "@"+a.Username))
	}
	rows = append(rows, pairRow("Phone", orDash(a.PhoneMasked)))
	rows = append(rows, pairRow("Linked", timeWithAge(a.LinkedAt, p.w.vm.Now())))
	if a.Connected {
		return append(rows, pairRow("Connection", "Connected"))
	}
	return append(rows, pairRow("Connection", "Disconnected"))
}

func (p *telegramPanel) confirmUnlink() {
	win := p.w.window()
	if win == nil {
		return
	}
	shell.ConfirmTyped(win, "Unlink Telegram", unlinkBody, vm.UnlinkWord, "Unlink", p.unlinkNow)
}

func (p *telegramPanel) unlinkNow() {
	p.w.run(func(ctx context.Context) {
		outcome := p.w.vm.Unlink(ctx, vm.UnlinkWord)
		p.w.post(func() { p.afterUnlink(outcome) })
	})
}

func (p *telegramPanel) afterUnlink(outcome vm.UnlinkOutcome) {
	if outcome != vm.UnlinkDone {
		return
	}
	for _, route := range gatedRoutes() {
		p.w.nav.SetEnabled(route, false, linkTelegramFirst)
	}
	p.w.nav.Navigate(outcome.Route(), nil)
}

func gatedRoutes() []model.Route {
	return []model.Route{model.RouteDashboard, model.RouteBuckets, model.RouteKeys, model.RouteObjects, model.RouteReplica}
}
