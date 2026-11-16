package telegramlink

import (
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

const countdownTick = time.Second

type WizardOptions struct {
	AskAppCredentials bool
	AllowReuseApp     bool
	Title             string
}

type LoginWizard struct {
	win  fyne.Window
	v    *vm.ViewModel
	opts WizardOptions
	n    shell.Notifier

	root     fyne.CanvasObject
	heading  *widget.Label
	stepName *widget.Label
	progress *widget.ProgressBarInfinite
	cancel   *widget.Button
	panels   map[vm.Step]fyne.CanvasObject
	inline   map[vm.Field]*widget.Label

	app      appPanel
	phone    phonePanel
	wait     waitPanel
	code     codePanel
	password passwordPanel
	done     donePanel

	shownStep   vm.Step
	seenProblem int
	seenFocus   int
	codeExpires time.Time
	unsubscribe func()

	mu       sync.Mutex
	ctx      context.Context
	onLinked func(adminv1.TelegramAccount)

	commands sync.WaitGroup
	loop     sync.WaitGroup
}

func NewLoginWizard(win fyne.Window, flow vm.LoginFlow, opts WizardOptions, n shell.Notifier) *LoginWizard {
	return NewLoginWizardFromViewModel(win, vm.New(flow, time.Now), opts, n)
}

func NewLoginWizardFromViewModel(win fyne.Window, v *vm.ViewModel, opts WizardOptions, n shell.Notifier) *LoginWizard {
	w := &LoginWizard{win: win, v: v, opts: opts, n: n, ctx: context.Background()}
	w.build()
	w.render(v.State().Get())
	w.unsubscribe = v.State().Subscribe(func(st vm.State) {
		fyne.Do(func() { w.render(st) })
	})
	return w
}

func (w *LoginWizard) Content() fyne.CanvasObject {
	return w.root
}

func (w *LoginWizard) Object() fyne.CanvasObject {
	return w.root
}

func (w *LoginWizard) OnLinked(fn func(adminv1.TelegramAccount)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onLinked = fn
}

func (w *LoginWizard) Start(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
	w.dispatch(w.v.Refresh)
	w.loop.Add(1)
	go w.run(ctx)
}

func (w *LoginWizard) Wait() {
	w.loop.Wait()
	w.commands.Wait()
}

func (w *LoginWizard) Close() {
	w.unsubscribe()
}

func (w *LoginWizard) ClearSecrets() {
	w.app.hash.SetText("")
	w.code.entry.SetText("")
	w.password.entry.SetText("")
}

func (w *LoginWizard) run(ctx context.Context) {
	defer w.loop.Done()
	ticker := time.NewTicker(countdownTick)
	defer ticker.Stop()
	lastPoll := w.v.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.v.Tick()
			fyne.Do(w.refreshCountdown)
			if now := w.v.Now(); now.Sub(lastPoll) >= w.v.Interval() {
				lastPoll = now
				w.dispatch(w.v.Poll)
			}
		}
	}
}

func (w *LoginWizard) dispatch(command func(ctx context.Context)) {
	w.commands.Add(1)
	go func() {
		defer w.commands.Done()
		command(w.context())
	}()
}

func (w *LoginWizard) context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctx
}

func (w *LoginWizard) finish() {
	w.mu.Lock()
	fn := w.onLinked
	w.mu.Unlock()
	account := w.v.State().Get().Account
	if fn == nil || account == nil {
		return
	}
	fn(*account)
}

func (w *LoginWizard) build() {
	w.heading = shell.NewHeading(w.title())
	w.stepName = shell.NewSectionTitle("")
	w.progress = widget.NewProgressBarInfinite()
	w.cancel = widget.NewButton(buttonCancelLogin, func() { w.dispatch(w.v.Cancel) })
	w.inline = make(map[vm.Field]*widget.Label)
	w.panels = map[vm.Step]fyne.CanvasObject{
		vm.StepApp:      w.buildApp(),
		vm.StepPhone:    w.buildPhone(),
		vm.StepWait:     w.buildWait(),
		vm.StepCode:     w.buildCode(),
		vm.StepPassword: w.buildPassword(),
		vm.StepDone:     w.buildDone(),
	}
	stack := container.NewStack()
	for _, panel := range w.panels {
		panel.Hide()
		stack.Add(panel)
	}
	header := shell.NewPageHeader(pageKicker, w.heading)
	body := container.NewVBox(header, w.stepName, stack, w.progress, shell.NewSectionGap(), container.NewHBox(shell.Action(w.cancel)))
	w.root = shell.ScrollBody(shell.ReadableColumn(body))
}

func (w *LoginWizard) title() string {
	if w.opts.Title != "" {
		return w.opts.Title
	}
	return defaultTitle
}
