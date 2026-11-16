package connect

import (
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

type panel interface {
	object() fyne.CanvasObject
	update(s vm.State)
	focusFirst()
	focusField(field string) bool
}

type view struct {
	vm   *vm.ViewModel
	nav  shell.Navigator
	clip model.Clipboard
	now  func() time.Time

	dispatch func(func())

	mu  sync.Mutex
	ctx context.Context

	notice   *widget.Label
	problem  *fyne.Container
	progress *widget.ProgressBarInfinite
	panels   map[vm.Step]panel
	stack    *fyne.Container
	root     fyne.CanvasObject
	lastStep vm.Step
	rendered renderedSecrets
}

type renderedSecrets struct {
	setupToken  string
	pairingCode string
}

func NewView(v *vm.ViewModel, nav shell.Navigator) fyne.CanvasObject {
	return newView(v, nav, appClipboard{}, timeNow, fyne.Do).root
}

func timeNow() time.Time {
	return time.Now()
}

func newView(v *vm.ViewModel, nav shell.Navigator, clip model.Clipboard, now func() time.Time, dispatch func(func())) *view {
	w := &view{vm: v, nav: nav, clip: clip, now: now, dispatch: dispatch, ctx: context.Background()}
	initial := v.State().Get()
	w.notice = newWrappedLabel("")
	w.notice.Hide()
	notice := shell.NewCallout(shell.CalloutInfo, w.notice)
	w.problem = container.NewVBox()
	w.progress = widget.NewProgressBarInfinite()
	w.progress.Hide()
	w.panels = map[vm.Step]panel{
		vm.StepProfiles:           newProfilesPanel(w),
		vm.StepConfirmCertificate: newCertificatePanel(w),
		vm.StepClaim:              newClaimPanel(w, initial.DefaultDeviceName),
		vm.StepPair:               newPairPanel(w, initial.DefaultDeviceName),
		vm.StepStorageChoice:      newStoragePanel(w),
		vm.StepPinMismatch:        newMismatchPanel(w),
	}
	w.stack = container.NewStack()
	for _, p := range w.panels {
		w.stack.Add(p.object())
	}
	body := container.NewVBox(
		shell.NewPageHeader(pageKicker, shell.NewHeading(pageTitle)),
		notice,
		w.problem,
		w.progress,
		w.stack,
	)
	w.root = shell.ScrollBody(shell.CentredColumn(body))
	w.render(initial)
	v.State().Subscribe(func(s vm.State) {
		w.dispatch(func() { w.render(s) })
	})
	return w
}

func (w *view) context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctx
}

func (w *view) bind(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
}

func (w *view) run(command func(ctx context.Context)) {
	ctx := w.context()
	go command(ctx)
}

func (w *view) render(s vm.State) {
	w.renderBanner(s)
	for step, p := range w.panels {
		if step == s.Step {
			p.update(s)
			p.object().Show()
			continue
		}
		p.object().Hide()
	}
	w.stack.Refresh()
	w.moveFocus(s)
	w.routeBlockingStep(s)
	w.rendered = renderedSecrets{setupToken: s.SetupToken, pairingCode: s.PairingCode}
	w.lastStep = s.Step
}

func (w *view) renderBanner(s vm.State) {
	w.notice.SetText(s.Notice)
	if s.Notice == "" {
		w.notice.Hide()
	} else {
		w.notice.Show()
	}
	w.problem.RemoveAll()
	if s.Problem != nil {
		w.problem.Add(container.NewVBox(shell.NewCallout(shell.CalloutDanger, shell.NewProblemView(*s.Problem, w.clip)), shell.NewSectionGap()))
	}
	w.problem.Refresh()
	if s.Loading {
		w.progress.Show()
	} else {
		w.progress.Hide()
	}
}

func (w *view) moveFocus(s vm.State) {
	active := w.panels[s.Step]
	if active == nil {
		return
	}
	if s.Problem != nil && s.Problem.Field != "" && active.focusField(s.Problem.Field) {
		return
	}
	if s.Step != w.lastStep {
		active.focusFirst()
	}
}

func (w *view) routeBlockingStep(s vm.State) {
	if w.nav == nil || s.Step == vm.StepProfiles || s.Step == w.lastStep {
		return
	}
	if w.nav.Current() != model.RouteConnect {
		w.nav.Navigate(model.RouteConnect, nil)
	}
}

func (w *view) stopTimers() {
	if pair, ok := w.panels[vm.StepPair].(*pairPanel); ok {
		pair.stopCountdown()
	}
}

func windowFor(object fyne.CanvasObject) fyne.Window {
	driver := fyne.CurrentApp().Driver()
	canvas := driver.CanvasForObject(object)
	if canvas == nil {
		return nil
	}
	for _, win := range driver.AllWindows() {
		if win.Canvas() == canvas {
			return win
		}
	}
	return nil
}
