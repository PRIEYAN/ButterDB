package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/projects"
	projectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/projects/vm"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink"
	telegramvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

const (
	jsonTimeout  = 75 * time.Second
	closeTimeout = 30 * time.Second
)

type session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	project embedded.Project
	api     *guardedClient
	models  sessionModels
	handled atomic.Bool
}

type composition struct {
	app    fyne.App
	win    *closeGuardWindow
	sh     *shell.Shell
	opts   Options
	clip   model.Clipboard
	prefs  fyne.Preferences
	ctx    context.Context
	cancel context.CancelFunc
	loop   *loopGate
	api    *adminv1.Client

	telegram   *telegramvm.ViewModel
	projectsVM *projectsvm.ViewModel
	gates      []shell.Screen
	closing    atomic.Int32

	mu   sync.Mutex
	sess *session
}

func Build(a fyne.App, w fyne.Window, opts Options) (*shell.Shell, error) {
	c, err := compose(a, w, opts, lifecycleStart(a))
	if err != nil {
		return nil, err
	}
	return c.sh, nil
}

func compose(a fyne.App, w fyne.Window, opts Options, afterLoopStarts func(func())) (*composition, error) {
	if a == nil {
		return nil, fmt.Errorf("app: %w: fyne app", ErrMissingDependency)
	}
	if w == nil {
		return nil, fmt.Errorf("app: %w: window", ErrMissingDependency)
	}
	if afterLoopStarts == nil {
		return nil, fmt.Errorf("app: %w: loop start hook", ErrMissingDependency)
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	api, err := adminv1.NewClient(adminv1.ClientConfig{
		BaseURL:       embedded.BaseURL,
		HTTPTransport: opts.Engine,
		UserAgent:     "butterdb-admin/" + opts.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")",
		JSONTimeout:   jsonTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("app: admin client: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &composition{
		app:    a,
		win:    newCloseGuardWindow(w),
		opts:   opts,
		clip:   clipboardAdapter{app: a},
		prefs:  a.Preferences(),
		ctx:    ctx,
		cancel: cancel,
		loop:   &loopGate{},
		api:    api,
	}
	a.Settings().SetTheme(settingsui.ThemeFor(c.prefs.StringWithFallback(settingsvm.PrefTheme, settingsvm.ThemeSystem)))
	c.sh = shell.New(a, w)
	if err := c.buildGates(); err != nil {
		cancel()
		return nil, err
	}
	c.win.bindQuitShortcut()
	w.SetOnClosed(c.shutdown)
	c.loop.whenOpen(func() { go c.startup() })
	afterLoopStarts(c.loop.release)
	return c, nil
}

func (c *composition) buildGates() error {
	flow, err := newPrimaryFlow(c.api)
	if err != nil {
		return err
	}
	c.telegram = telegramvm.New(flow, c.opts.Now)
	wizard := telegramlink.NewLoginWizardFromViewModel(c.win, c.telegram, telegramlink.WizardOptions{AskAppCredentials: true}, c.sh.Notifier())
	link := telegramlink.NewScreen(wizard)
	link.OnLinked(func(adminv1.TelegramAccount) { c.sh.Navigator().Navigate(model.RouteProjects, nil) })
	c.projectsVM = projectsvm.New(c.opts.Engine, c.opts.Now)
	picker := projects.NewScreen(c.projectsVM, c.sh.Notifier(), c.onOpened)
	c.gates = []shell.Screen{
		hookedScreen{Screen: link, life: c.ctx, beforeShow: c.enterGate(model.RouteTelegram), whenLoopRuns: c.loop.whenOpen},
		hookedScreen{Screen: picker, life: c.ctx, beforeShow: c.enterGate(model.RouteProjects), whenLoopRuns: c.loop.whenOpen},
	}
	for _, g := range c.gates {
		c.sh.Register(g)
	}
	return nil
}

func (c *composition) startup() {
	status, err := c.api.TelegramStatus(c.ctx)
	if c.ctx.Err() != nil {
		return
	}
	route := model.RouteTelegram
	if err == nil && status.State == adminv1.TelegramLinked {
		route = model.RouteProjects
	}
	fyne.Do(func() { c.sh.Navigator().Navigate(route, nil) })
}

func (c *composition) shutdown() {
	c.cancel()
}

func (c *composition) currentSession() *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *composition) endSession() bool {
	c.mu.Lock()
	s := c.sess
	c.sess = nil
	c.mu.Unlock()
	if s == nil {
		return false
	}
	s.cancel()
	return true
}

func (c *composition) resetShell() {
	c.sh.Reset()
	for _, g := range c.gates {
		c.sh.Register(g)
	}
}

func (c *composition) enterGate(route model.Route) func(context.Context, model.Params) bool {
	return func(_ context.Context, p model.Params) bool {
		if !c.endSession() {
			return c.closing.Load() == 0
		}
		c.resetShell()
		c.closing.Add(1)
		go func() {
			ctx, cancel := context.WithTimeout(c.ctx, closeTimeout)
			_ = c.opts.Engine.CloseProject(ctx)
			cancel()
			fyne.Do(func() {
				c.closing.Add(-1)
				c.sh.Navigator().Navigate(route, p)
			})
		}()
		return false
	}
}

func (c *composition) onOpened(p embedded.Project) {
	fyne.Do(func() { c.activate(p) })
}

func (c *composition) sessionError(s *session, err error) {
	if s.ctx.Err() != nil || !s.handled.CompareAndSwap(false, true) {
		return
	}
	fyne.Do(func() {
		if s.ctx.Err() == nil {
			c.sh.Navigator().Navigate(model.RouteTelegram, nil)
		}
	})
}

func (c *composition) abandon(err error) {
	problem := model.Describe(err, c.opts.Now())
	fyne.Do(func() { c.sh.Notifier().Problem(problem) })
}
