package app

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets"
	bucketsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard"
	dashboardvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys"
	keysvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects"
	objectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink"
	telegramvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

type sessionModels struct {
	telegram  *telegramvm.ViewModel
	dashboard *dashboardvm.ViewModel
	buckets   *bucketsvm.ViewModel
	keys      *keysvm.ViewModel
	objects   *objectsvm.ViewModel
	settings  *settingsvm.ViewModel
}

func gatedRoutes() []model.Route {
	return []model.Route{model.RouteDashboard, model.RouteBuckets, model.RouteKeys, model.RouteObjects}
}

func restorable(r model.Route) bool {
	switch r {
	case model.RouteDashboard, model.RouteBuckets, model.RouteKeys, model.RouteObjects, model.RouteSettings:
		return true
	}
	return false
}

func (c *composition) activate(s *session, status adminv1.TelegramStatus) {
	if s.ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	previous := c.sess
	c.sess = s
	c.mu.Unlock()
	if previous != nil {
		previous.cancel()
		c.resetShell()
	}
	screens, err := c.buildScreens(s)
	if err != nil {
		c.endSession()
		c.abandon(s, err)
		return
	}
	for _, sc := range screens {
		c.sh.Register(c.tracked(s, sc))
	}
	c.sh.SetServerLabel(s.profile.Label, true)
	if status.State == adminv1.TelegramLinked {
		c.enterLinked(s, true)
		return
	}
	c.enterUnlinked()
}

func (c *composition) tracked(s *session, sc shell.Screen) shell.Screen {
	route := sc.Route()
	return hookedScreen{Screen: sc, beforeShow: func(context.Context, model.Params) bool {
		if restorable(route) {
			c.prefs.SetString(settingsvm.LastRouteKey(s.profile.ServerID), string(route))
		}
		return true
	}}
}

func (c *composition) enterUnlinked() {
	nav := c.sh.Navigator()
	for _, r := range gatedRoutes() {
		nav.SetEnabled(r, false, reasonLinkFirst)
	}
	nav.SetEnabled(model.RouteTelegram, true, "")
	nav.Navigate(model.RouteTelegram, nil)
}

func (c *composition) enterLinked(s *session, restore bool) {
	nav := c.sh.Navigator()
	nav.SetEnabled(model.RouteTelegram, false, "")
	for _, r := range gatedRoutes() {
		nav.SetEnabled(r, true, "")
	}
	target := model.RouteDashboard
	if restore {
		target = c.lastRoute(s)
	}
	nav.Navigate(target, nil)
}

func (c *composition) lastRoute(s *session) model.Route {
	stored := model.Route(c.prefs.StringWithFallback(settingsvm.LastRouteKey(s.profile.ServerID), string(model.RouteDashboard)))
	if !restorable(stored) {
		return model.RouteDashboard
	}
	return stored
}

func (c *composition) buildScreens(s *session) ([]shell.Screen, error) {
	nav := c.sh.Navigator()
	notifier := c.sh.Notifier()
	now := c.opts.Now
	window := func() fyne.Window { return c.win }

	flow, err := newPrimaryFlow(s.api)
	if err != nil {
		return nil, err
	}
	models := sessionModels{
		telegram:  telegramvm.New(flow, now),
		dashboard: dashboardvm.New(s.api, now),
		buckets:   bucketsvm.New(s.api, now),
		keys:      keysvm.New(s.api, now),
	}
	wizard := telegramlink.NewLoginWizardFromViewModel(c.win, models.telegram, telegramlink.WizardOptions{AskAppCredentials: true}, notifier)
	linkScreen := telegramlink.NewScreen(wizard)
	linkScreen.OnLinked(func(adminv1.TelegramAccount) { c.enterLinked(s, false) })

	objectOptions := []objectsvm.Option{objectsvm.WithUploadConcurrency(c.prefs.IntWithFallback(settingsvm.PrefUploadConcurrency, settingsvm.DefaultUploadConcurrency))}
	if c.opts.Random != nil {
		objectOptions = append(objectOptions, objectsvm.WithRandom(c.opts.Random))
	}
	models.objects = objectsvm.New(s.api, now, objectOptions...)
	objectsScreen := objects.NewScreen(models.objects, nav, c.win, objects.Options{
		Context:     s.ctx,
		Notifier:    notifier,
		Clipboard:   c.clip,
		Now:         now,
		DownloadDir: c.downloadDir,
	})

	models.settings, err = c.settingsViewModel(s)
	if err != nil {
		return nil, err
	}
	s.models = models

	return []shell.Screen{
		linkScreen,
		dashboard.NewScreen(models.dashboard, nav, notifier, c.clip),
		buckets.NewScreenWithEnv(models.buckets, nav, buckets.Env{Window: window, Clipboard: c.clip}),
		keys.NewScreenWithEnv(models.keys, nav, keys.Env{Window: window, Clipboard: c.clip}),
		objectsScreen,
		settingsui.NewScreen(models.settings, nav, notifier, c.clip, c.win),
	}, nil
}

func (c *composition) downloadDir() string {
	return c.prefs.StringWithFallback(settingsvm.PrefDownloadDir, c.opts.DownloadDir)
}

func (c *composition) settingsViewModel(s *session) (*settingsvm.ViewModel, error) {
	var keychain model.TokenStore
	if keychainUsable(c.opts.Tokens) {
		keychain = c.opts.Tokens
	}
	prober, ok := c.opts.Dialer.(settingsvm.CertificateProber)
	if !ok {
		return nil, fmt.Errorf("app: dialer cannot probe certificates")
	}
	return settingsvm.New(s.api, c.opts.Now, settingsvm.Session{
		Profile:            s.profile,
		Tokens:             c.opts.tokenStoreFor(s.profile.TokenStorage),
		Profiles:           c.opts.Store,
		Keychain:           keychain,
		Prober:             prober,
		SetClientToken:     s.api.SetToken,
		Preferences:        c.prefs,
		AppVersion:         c.opts.Version,
		DefaultDownloadDir: c.opts.DownloadDir,
	}), nil
}
