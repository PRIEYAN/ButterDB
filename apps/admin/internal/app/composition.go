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
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect"
	connectvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

const (
	reasonLinkFirst = "Link Telegram first"
	jsonTimeout     = 75 * time.Second
)

type session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	profile model.Profile
	api     *guardedClient
	models  sessionModels
	handled atomic.Bool
}

type composition struct {
	app       fyne.App
	win       *closeGuardWindow
	sh        *shell.Shell
	opts      Options
	clip      model.Clipboard
	prefs     fyne.Preferences
	ctx       context.Context
	cancel    context.CancelFunc
	connectVM *connectvm.ViewModel
	gate      shell.Screen

	mu   sync.Mutex
	sess *session
}

func Build(a fyne.App, w fyne.Window, opts Options) (*shell.Shell, error) {
	c, err := compose(a, w, opts)
	if err != nil {
		return nil, err
	}
	return c.sh, nil
}

func compose(a fyne.App, w fyne.Window, opts Options) (*composition, error) {
	if a == nil {
		return nil, fmt.Errorf("app: %w: fyne app", ErrMissingDependency)
	}
	if w == nil {
		return nil, fmt.Errorf("app: %w: window", ErrMissingDependency)
	}
	if err := opts.validate(); err != nil {
		return nil, err
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
	}
	a.Settings().SetTheme(settingsui.ThemeFor(c.prefs.StringWithFallback(settingsvm.PrefTheme, settingsvm.ThemeSystem)))
	c.sh = shell.New(a, w)
	if err := c.buildConnect(); err != nil {
		cancel()
		return nil, err
	}
	c.win.bindQuitShortcut()
	w.SetOnClosed(c.shutdown)
	c.sh.Navigator().Navigate(model.RouteConnect, nil)
	go c.connectVM.AutoConnect(c.ctx)
	return c, nil
}

func (c *composition) buildConnect() error {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	v, err := connectvm.New(connectvm.Dependencies{
		Dialer: c.opts.Dialer,
		Tokens: connectvm.TokenStores{
			Keychain: c.opts.Tokens,
			File:     c.opts.Files,
			Memory:   c.opts.Memory,
		},
		Profiles:          c.opts.Store,
		Now:               c.opts.Now,
		OnConnected:       c.onConnected,
		DefaultDeviceName: c.opts.DeviceName,
		Platform:          platform,
		ClientVersion:     c.opts.Version,
		UserAgent:         "butterdb-admin/" + c.opts.Version + " (" + platform + ")",
	})
	if err != nil {
		return fmt.Errorf("app: connect view model: %w", err)
	}
	c.connectVM = v
	c.gate = hookedScreen{Screen: connect.NewScreen(v, c.sh.Navigator()), beforeShow: c.enterConnect}
	c.sh.Register(c.gate)
	return nil
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
	c.sh.Register(c.gate)
}

func (c *composition) enterConnect(_ context.Context, p model.Params) bool {
	if !c.endSession() {
		return true
	}
	c.resetShell()
	c.sh.Navigator().Navigate(model.RouteConnect, p)
	return false
}

func (c *composition) onConnected(p model.Profile) {
	s, status, err := c.openSession(p)
	if err != nil {
		c.abandon(s, err)
		return
	}
	fyne.Do(func() { c.activate(s, status) })
}

func (c *composition) openSession(p model.Profile) (*session, adminv1.TelegramStatus, error) {
	token, err := c.opts.tokenStoreFor(p.TokenStorage).Get(p.ServerID)
	if err != nil {
		return nil, adminv1.TelegramStatus{}, fmt.Errorf("app: read device token: %w", err)
	}
	dialed, err := c.opts.Dialer.Dial(adminv1.ClientConfig{
		BaseURL:     p.BaseURL,
		Trust:       p.Trust,
		Token:       token,
		UserAgent:   "butterdb-admin/" + c.opts.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")",
		JSONTimeout: jsonTimeout,
	})
	if err != nil {
		return nil, adminv1.TelegramStatus{}, fmt.Errorf("app: dial: %w", err)
	}
	raw, ok := dialed.(sessionAPI)
	if !ok {
		return nil, adminv1.TelegramStatus{}, fmt.Errorf("app: dialer returned a client without session methods")
	}
	ctx, cancel := context.WithCancel(c.ctx)
	s := &session{ctx: ctx, cancel: cancel, profile: p}
	guarded, err := newGuardedClient(raw, func(err error) { c.sessionError(s, err) })
	if err != nil {
		cancel()
		return nil, adminv1.TelegramStatus{}, err
	}
	s.api = guarded
	if _, err := guarded.ServerInfo(ctx); err != nil {
		return s, adminv1.TelegramStatus{}, err
	}
	status, err := guarded.TelegramStatus(ctx)
	if err != nil {
		return s, adminv1.TelegramStatus{}, err
	}
	return s, status, nil
}

func (c *composition) sessionError(s *session, err error) {
	if s.ctx.Err() != nil || !s.handled.CompareAndSwap(false, true) {
		return
	}
	c.connectVM.HandleSessionError(s.ctx, s.profile.ServerID, err)
	s.cancel()
	fyne.Do(func() { c.sh.Navigator().Navigate(model.RouteConnect, nil) })
}

func (c *composition) abandon(s *session, err error) {
	if s != nil {
		if s.handled.Load() {
			return
		}
		s.cancel()
	}
	problem := model.Describe(err, c.opts.Now())
	fyne.Do(func() { c.sh.Notifier().Problem(problem) })
}
