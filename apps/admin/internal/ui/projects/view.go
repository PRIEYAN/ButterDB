package projects

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/projects/vm"
)

const (
	pageKicker      = "butterDB / projects"
	textEmpty       = "No projects yet. Create one to start storing files in your Telegram."
	textCreateWarn  = "If you forget this password and lose every device that has this project open, encrypted files cannot be recovered."
	textUnlockNote  = "This project was set up on another device. If it is open there too, both devices can change the same file at the same moment and one change can be lost."
	textResetWarn   = "A new key is made for this project. Encrypted files stored with the old key can no longer be read, and every access key of this project is revoked."
	textNothingLost = "No encrypted files were lost."
	timeLayout      = "2006-01-02 15:04"

	PlaceholderName         = "Project name"
	PlaceholderPassword     = "Password"
	PlaceholderConfirm      = "Confirm password"
	PlaceholderNewPassword  = "New password"
	PlaceholderConfirmNew   = "Confirm new password"
	PlaceholderConfirmation = "Type " + vm.ResetConfirmation + " to confirm"
)

type view struct {
	vm       *vm.ViewModel
	notifier shell.Notifier
	onOpened func(vm.Project)
	post     func(func())

	root    fyne.CanvasObject
	panels  *fyne.Container
	busy    *fyne.Container
	busyTxt *widget.Label

	list   *listPanel
	create *createPanel
	unlock *unlockPanel
	reset  *resetPanel
	report *reportPanel

	mu          sync.Mutex
	ctx         context.Context
	lastProblem *model.Problem
}

type listPanel struct {
	root  fyne.CanvasObject
	rows  *fyne.Container
	empty *widget.Label
}

type createPanel struct {
	root    fyne.CanvasObject
	name    *widget.Entry
	pass    *widget.Entry
	confirm *widget.Entry
	errText *widget.Label
	submit  *widget.Button
}

type unlockPanel struct {
	root    fyne.CanvasObject
	title   *widget.Label
	pass    *widget.Entry
	errText *widget.Label
	submit  *widget.Button
}

type resetPanel struct {
	root    fyne.CanvasObject
	title   *widget.Label
	pass    *widget.Entry
	confirm *widget.Entry
	typed   *widget.Entry
	errText *widget.Label
	submit  *widget.Button
}

type reportPanel struct {
	root fyne.CanvasObject
	rows *fyne.Container
}

func newView(v *vm.ViewModel, n shell.Notifier, onOpened func(vm.Project)) *view {
	w := &view{vm: v, notifier: n, onOpened: onOpened, post: fyne.Do, ctx: context.Background()}
	w.build()
	w.render(v.State().Get())
	v.State().Subscribe(func(s vm.State) {
		w.post(func() { w.render(s) })
	})
	return w
}

func (w *view) build() {
	w.list = w.buildList()
	w.create = w.buildCreate()
	w.unlock = w.buildUnlock()
	w.reset = w.buildReset()
	w.report = w.buildReport()
	w.busyTxt = widget.NewLabel("")
	w.busy = container.NewVBox(widget.NewProgressBarInfinite(), w.busyTxt)
	w.busy.Hide()
	w.panels = container.NewStack(w.list.root, w.create.root, w.unlock.root, w.reset.root, w.report.root)
	header := shell.NewPageHeader(pageKicker, shell.NewHeading("Projects"))
	body := container.NewBorder(w.busy, nil, nil, nil, w.panels)
	w.root = container.NewBorder(header, nil, nil, nil, shell.CentredColumn(body))
}

func (w *view) buildList() *listPanel {
	p := &listPanel{rows: container.NewVBox(), empty: wrapped(textEmpty)}
	p.empty.Hide()
	newProject := widget.NewButtonWithIcon("New project", theme.ContentAddIcon(), w.vm.StartCreate)
	newProject.Importance = widget.HighImportance
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() { w.run(w.vm.Refresh) })
	toolbar := shell.Actions(newProject, refresh)
	content := container.NewVBox(toolbar, shell.NewSectionGap(), p.empty, p.rows)
	p.root = shell.ScrollBody(shell.NewWindowCard("projects/list", "Your projects", content))
	return p
}

func (w *view) buildCreate() *createPanel {
	p := &createPanel{
		name:    entry(PlaceholderName),
		pass:    passwordEntry(PlaceholderPassword),
		confirm: passwordEntry(PlaceholderConfirm),
		errText: errorLabel(),
	}
	p.submit = widget.NewButtonWithIcon("Create project", theme.ConfirmIcon(), w.submitCreate)
	p.submit.Importance = widget.HighImportance
	p.confirm.OnSubmitted = func(string) { w.submitCreate() }
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), w.back)
	content := container.NewVBox(
		shell.NewFieldLabel("Project name"), p.name,
		shell.NewFieldLabel("Password"), p.pass,
		shell.NewFieldLabel("Confirm password"), p.confirm,
		shell.NewSectionGap(),
		shell.NewCalloutText(shell.CalloutWarning, textCreateWarn),
		p.errText,
		shell.NewSectionGap(),
		shell.Actions(p.submit, cancel),
	)
	p.root = shell.ScrollBody(shell.NewWindowCard("projects/new", "New project", content))
	return p
}

func (w *view) buildUnlock() *unlockPanel {
	p := &unlockPanel{title: shell.NewSectionTitle(""), pass: passwordEntry(PlaceholderPassword), errText: errorLabel()}
	p.submit = widget.NewButtonWithIcon("Unlock", theme.ConfirmIcon(), w.submitUnlock)
	p.submit.Importance = widget.HighImportance
	p.pass.OnSubmitted = func(string) { w.submitUnlock() }
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), w.back)
	forgot := widget.NewButton("Forgot password? Reset project key", func() {
		w.vm.StartReset(w.vm.State().Get().Target.ID)
	})
	forgot.Importance = widget.LowImportance
	content := container.NewVBox(
		p.title,
		shell.NewCalloutText(shell.CalloutWarning, textUnlockNote),
		shell.NewSectionGap(),
		shell.NewFieldLabel("Password"), p.pass,
		p.errText,
		shell.NewSectionGap(),
		shell.Actions(p.submit, cancel),
		container.NewHBox(forgot),
	)
	p.root = shell.ScrollBody(shell.NewWindowCard("projects/unlock", "", content))
	return p
}

func (w *view) buildReset() *resetPanel {
	p := &resetPanel{
		title:   shell.NewSectionTitle(""),
		pass:    passwordEntry(PlaceholderNewPassword),
		confirm: passwordEntry(PlaceholderConfirmNew),
		typed:   entry(PlaceholderConfirmation),
		errText: errorLabel(),
	}
	p.submit = widget.NewButtonWithIcon("Reset project key", theme.WarningIcon(), w.submitReset)
	p.submit.Importance = widget.DangerImportance
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), w.back)
	content := container.NewVBox(
		p.title,
		shell.NewCalloutText(shell.CalloutDanger, textResetWarn),
		shell.NewSectionGap(),
		shell.NewFieldLabel("New password"), p.pass,
		shell.NewFieldLabel("Confirm new password"), p.confirm,
		shell.NewFieldLabel(PlaceholderConfirmation), p.typed,
		p.errText,
		shell.NewSectionGap(),
		shell.Actions(p.submit, cancel),
	)
	p.root = shell.ScrollBody(shell.NewWindowCard("projects/reset", "", content))
	return p
}

func (w *view) buildReport() *reportPanel {
	p := &reportPanel{rows: container.NewVBox()}
	open := widget.NewButtonWithIcon("Open project", theme.NavigateNextIcon(), func() {
		w.openWith(w.vm.OpenTarget)
	})
	open.Importance = widget.HighImportance
	content := container.NewVBox(p.rows, shell.NewSectionGap(), shell.Actions(open))
	p.root = shell.ScrollBody(shell.NewWindowCard("projects/report", "Project key reset", content))
	return p
}

func (w *view) setContext(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
}

func (w *view) context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctx
}

func (w *view) run(fn func(ctx context.Context)) {
	ctx := w.context()
	go fn(ctx)
}

func (w *view) back() {
	w.clearSecrets()
	w.vm.Back()
	w.run(w.vm.Refresh)
}

func (w *view) openWith(fn func(ctx context.Context) (vm.Project, bool)) {
	ctx := w.context()
	go func() {
		if p, ok := fn(ctx); ok {
			w.opened(p)
		}
	}()
}

func (w *view) opened(p vm.Project) {
	if w.onOpened != nil {
		w.onOpened(p)
	}
}

func (w *view) submitCreate() {
	name := w.create.name.Text
	pass, confirm := []byte(w.create.pass.Text), []byte(w.create.confirm.Text)
	w.create.pass.SetText("")
	w.create.confirm.SetText("")
	w.openWith(func(ctx context.Context) (vm.Project, bool) { return w.vm.Create(ctx, name, pass, confirm) })
}

func (w *view) submitUnlock() {
	pass := []byte(w.unlock.pass.Text)
	w.unlock.pass.SetText("")
	w.openWith(func(ctx context.Context) (vm.Project, bool) { return w.vm.Unlock(ctx, pass) })
}

func (w *view) submitReset() {
	pass, confirm := []byte(w.reset.pass.Text), []byte(w.reset.confirm.Text)
	typed := w.reset.typed.Text
	w.reset.pass.SetText("")
	w.reset.confirm.SetText("")
	w.run(func(ctx context.Context) { w.vm.Reset(ctx, pass, confirm, typed) })
}

func (w *view) clearSecrets() {
	for _, e := range []*widget.Entry{w.create.pass, w.create.confirm, w.unlock.pass, w.reset.pass, w.reset.confirm} {
		e.SetText("")
	}
}

func (w *view) render(s vm.State) {
	w.renderBusy(s)
	w.renderList(s)
	w.unlock.title.SetText("Unlock " + s.Target.Name)
	w.reset.title.SetText("Reset the key of " + s.Target.Name)
	w.renderReport(s)
	panels := map[vm.Mode]fyne.CanvasObject{
		vm.ModeList: w.list.root, vm.ModeCreate: w.create.root, vm.ModeUnlock: w.unlock.root,
		vm.ModeReset: w.reset.root, vm.ModeReport: w.report.root,
	}
	for mode, panel := range panels {
		if mode == s.Mode {
			panel.Show()
			continue
		}
		panel.Hide()
	}
	w.renderProblem(s)
}

func (w *view) renderBusy(s vm.State) {
	if s.Busy == "" {
		w.busy.Hide()
		w.panels.Show()
		return
	}
	w.busyTxt.SetText(s.Busy)
	w.busy.Show()
	w.panels.Hide()
}

func (w *view) renderList(s vm.State) {
	if s.Loaded && len(s.Rows) == 0 {
		w.list.empty.Show()
	} else {
		w.list.empty.Hide()
	}
	rows := make([]fyne.CanvasObject, 0, len(s.Rows)*2)
	for _, row := range s.Rows {
		rows = append(rows, w.projectRow(row), widget.NewSeparator())
	}
	w.list.rows.Objects = rows
	w.list.rows.Refresh()
}

func (w *view) projectRow(row vm.Row) fyne.CanvasObject {
	p := row.Project
	name := shell.NewFieldLabel(p.Name)
	details := shell.NewKicker(projectDetails(p))
	left := container.NewVBox(container.NewHBox(name, shell.NewValueBadge(row.Badge)), details)
	action := w.rowAction(row)
	if action == nil {
		return left
	}
	return container.NewBorder(nil, nil, nil, container.NewVBox(action), left)
}

func (w *view) rowAction(row vm.Row) fyne.CanvasObject {
	id := row.Project.ID
	switch row.Action {
	case vm.ActionOpen:
		return shell.Action(widget.NewButtonWithIcon("Open", theme.NavigateNextIcon(), func() {
			w.openWith(func(ctx context.Context) (vm.Project, bool) { return w.vm.Open(ctx, id) })
		}))
	case vm.ActionFinish:
		return shell.Action(widget.NewButtonWithIcon("Finish setup", theme.NavigateNextIcon(), func() {
			w.openWith(func(ctx context.Context) (vm.Project, bool) { return w.vm.Open(ctx, id) })
		}))
	case vm.ActionUnlock:
		return shell.Action(widget.NewButtonWithIcon("Unlock", theme.LoginIcon(), func() { w.vm.StartUnlock(id) }))
	}
	return nil
}

func projectDetails(p vm.Project) string {
	text := strconv.Itoa(p.LiveBuckets) + " buckets"
	if p.LiveBuckets == 1 {
		text = "1 bucket"
	}
	if !p.LastOpened.IsZero() {
		text += " · last opened " + p.LastOpened.In(time.Local).Format(timeLayout)
	}
	return text
}

func (w *view) renderReport(s vm.State) {
	if s.Report == nil {
		w.report.rows.Objects = nil
		w.report.rows.Refresh()
		return
	}
	w.report.rows.Objects = reportRows(*s.Report)
	w.report.rows.Refresh()
}

func reportRows(r vm.ResetReport) []fyne.CanvasObject {
	buckets := make([]string, 0, len(r.UnreadableObjects))
	for b, n := range r.UnreadableObjects {
		if n > 0 {
			buckets = append(buckets, b)
		}
	}
	sort.Strings(buckets)
	rows := []fyne.CanvasObject{}
	for _, b := range buckets {
		rows = append(rows, wrapped(b+": "+strconv.FormatInt(r.UnreadableObjects[b], 10)+" encrypted files can no longer be read."))
	}
	if len(buckets) == 0 {
		rows = append(rows, wrapped(textNothingLost))
	}
	keys := strconv.Itoa(r.RevokedAccessKeys) + " access keys were revoked."
	if r.RevokedAccessKeys == 1 {
		keys = "1 access key was revoked."
	}
	return append(rows, wrapped(keys))
}

func (w *view) renderProblem(s vm.State) {
	inline := ""
	if s.Problem != nil && s.Problem.Presentation == model.PresentInline {
		inline = s.Problem.Title
	}
	for _, l := range []*widget.Label{w.create.errText, w.unlock.errText, w.reset.errText} {
		setError(l, inline)
	}
	if s.Problem == nil || s.Problem == w.lastProblem {
		w.lastProblem = s.Problem
		return
	}
	w.lastProblem = s.Problem
	if s.Problem.Presentation != model.PresentInline && w.notifier != nil {
		w.notifier.Problem(*s.Problem)
	}
}

func setError(l *widget.Label, text string) {
	l.SetText(text)
	if text == "" {
		l.Hide()
		return
	}
	l.Show()
}

func entry(placeholder string) *widget.Entry {
	e := widget.NewEntry()
	e.SetPlaceHolder(placeholder)
	return e
}

func passwordEntry(placeholder string) *widget.Entry {
	e := widget.NewPasswordEntry()
	e.SetPlaceHolder(placeholder)
	return e
}

func errorLabel() *widget.Label {
	l := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.Importance = widget.DangerImportance
	l.Wrapping = fyne.TextWrapWord
	l.Hide()
	return l
}

func wrapped(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}
