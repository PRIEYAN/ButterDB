package settings

import (
	"context"
	"sort"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

const confirmationPlaceholder = "Type " + vm.ResetConfirmation + " to confirm"

type projectPanel struct {
	w        *view
	root     fyne.CanvasObject
	facts    *fyne.Container
	name     *widget.Entry
	oldPass  *widget.Entry
	newPass  *widget.Entry
	confirm  *widget.Entry
	resetNew *widget.Entry
	resetCon *widget.Entry
	typed    *widget.Entry
	report   *fyne.Container
	lastName string
}

func newProjectPanel(w *view) *projectPanel {
	p := &projectPanel{
		w:        w,
		facts:    container.NewVBox(),
		report:   container.NewVBox(),
		name:     plainEntry("Project name"),
		oldPass:  secretEntry("Current password"),
		newPass:  secretEntry("New password"),
		confirm:  secretEntry("Confirm new password"),
		resetNew: secretEntry("New password"),
		resetCon: secretEntry("Confirm new password"),
		typed:    plainEntry(confirmationPlaceholder),
	}
	rename := widget.NewButtonWithIcon("Rename project", theme.DocumentSaveIcon(), p.onRename)
	p.name.OnSubmitted = func(string) { p.onRename() }
	change := widget.NewButtonWithIcon("Change project password", theme.ConfirmIcon(), p.onChangePassword)
	reset := widget.NewButtonWithIcon("Reset project key", theme.WarningIcon(), p.onReset)
	reset.Importance = widget.DangerImportance
	switchProject := widget.NewButtonWithIcon("Switch project", theme.LogoutIcon(), func() {
		w.nav.Navigate(model.RouteProjects, nil)
	})
	p.root = scrolledCard(stepProject, "Project", container.NewVBox(
		p.facts,
		shell.NewSectionGap(),
		container.NewBorder(nil, nil, nameCell("Name"), shell.Action(rename), p.name),
		shell.NewSectionGap(),
		shell.NewSectionTitle("Change project password"),
		p.oldPass, p.newPass, p.confirm,
		container.NewHBox(shell.Action(change)),
		shell.NewSectionGap(),
		shell.NewSectionTitle("Reset project key"),
		shell.NewCalloutText(shell.CalloutDanger, resetKeyBody),
		p.resetNew, p.resetCon,
		heading(confirmationPlaceholder), p.typed,
		container.NewHBox(shell.Action(reset)),
		p.report,
		shell.NewSectionGap(),
		container.NewHBox(shell.Action(switchProject)),
	))
	return p
}

func (p *projectPanel) object() fyne.CanvasObject { return p.root }

func (p *projectPanel) onRename() {
	text := p.name.Text
	p.w.run(func(ctx context.Context) { p.w.vm.RenameProject(ctx, text) })
}

func (p *projectPanel) onChangePassword() {
	old, next, confirm := []byte(p.oldPass.Text), []byte(p.newPass.Text), []byte(p.confirm.Text)
	for _, e := range []*widget.Entry{p.oldPass, p.newPass, p.confirm} {
		e.SetText("")
	}
	p.w.run(func(ctx context.Context) { p.w.vm.ChangePassword(ctx, old, next, confirm) })
}

func (p *projectPanel) onReset() {
	next, confirm, typed := []byte(p.resetNew.Text), []byte(p.resetCon.Text), p.typed.Text
	p.resetNew.SetText("")
	p.resetCon.SetText("")
	p.w.run(func(ctx context.Context) {
		if _, ok := p.w.vm.ResetKey(ctx, next, confirm, typed); ok {
			p.w.post(func() { p.typed.SetText("") })
		}
	})
}

func (p *projectPanel) clearSecrets() {
	for _, e := range []*widget.Entry{p.oldPass, p.newPass, p.confirm, p.resetNew, p.resetCon} {
		e.SetText("")
	}
}

func (p *projectPanel) update(s vm.State) {
	proj := s.Project
	if !proj.Open {
		fill(p.facts, []fyne.CanvasObject{widget.NewLabel("No project is open.")})
		return
	}
	if p.lastName != proj.Project.Name {
		p.lastName = proj.Project.Name
		p.name.SetText(proj.Project.Name)
	}
	fill(p.facts, []fyne.CanvasObject{
		monoPairRow("Project ID", string(proj.Project.ID)),
		monoPairRow("Buckets", strconv.Itoa(proj.Project.LiveBuckets)),
	})
	fill(p.report, reportRows(proj.Report))
}

func reportRows(r *vm.ResetReport) []fyne.CanvasObject {
	if r == nil {
		return nil
	}
	buckets := make([]string, 0, len(r.UnreadableObjects))
	for b, n := range r.UnreadableObjects {
		if n > 0 {
			buckets = append(buckets, b)
		}
	}
	sort.Strings(buckets)
	rows := []fyne.CanvasObject{shell.NewSectionGap(), heading("Key reset")}
	for _, b := range buckets {
		rows = append(rows, newWrapped(b+": "+strconv.FormatInt(r.UnreadableObjects[b], 10)+" encrypted files can no longer be read."))
	}
	if len(buckets) == 0 {
		rows = append(rows, newWrapped("No encrypted files were lost."))
	}
	return append(rows, newWrapped(strconv.Itoa(r.RevokedAccessKeys)+" access keys revoked."))
}

func plainEntry(placeholder string) *widget.Entry {
	e := widget.NewEntry()
	e.SetPlaceHolder(placeholder)
	return e
}

func secretEntry(placeholder string) *widget.Entry {
	e := widget.NewPasswordEntry()
	e.SetPlaceHolder(placeholder)
	return e
}
