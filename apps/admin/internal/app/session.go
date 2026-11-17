package app

import (
	"context"
	"crypto/rand"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets"
	bucketsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard"
	dashboardvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects"
	objectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
	settingsui "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings"
	settingsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/viewer"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

type sessionModels struct {
	dashboard *dashboardvm.ViewModel
	buckets   *bucketsvm.ViewModel
	objects   *objectsvm.ViewModel
	settings  *settingsvm.ViewModel
}

func (c *composition) activate(p embedded.Project) {
	if c.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	s := &session{ctx: ctx, cancel: cancel, project: p}
	guarded, err := newGuardedClient(c.api, func(err error) { c.sessionError(s, err) })
	if err != nil {
		cancel()
		c.abandon(err)
		return
	}
	s.api = guarded
	c.mu.Lock()
	previous := c.sess
	c.sess = s
	c.mu.Unlock()
	if previous != nil {
		previous.cancel()
		c.resetShell()
	}
	for _, sc := range c.buildScreens(s) {
		c.sh.Register(hookedScreen{Screen: sc, life: s.ctx})
	}
	c.sh.SetProjectLabel(p.Name)
	c.followProjectName(s)
	c.sh.Navigator().Navigate(model.RouteDashboard, nil)
}

func (c *composition) followProjectName(s *session) {
	name := s.project.Name
	cancel := s.models.settings.State().Subscribe(func(st settingsvm.State) {
		if !st.Project.Open || st.Project.Project.Name == "" || st.Project.Project.Name == name {
			return
		}
		name = st.Project.Project.Name
		renamed := name
		fyne.Do(func() {
			if s.ctx.Err() == nil {
				c.sh.SetProjectLabel(renamed)
			}
		})
	})
	go func() {
		<-s.ctx.Done()
		cancel()
	}()
}

func (c *composition) buildScreens(s *session) []shell.Screen {
	nav := c.sh.Navigator()
	notifier := c.sh.Notifier()
	now := c.opts.Now
	window := func() fyne.Window { return c.win }

	models := sessionModels{
		dashboard: dashboardvm.New(s.api, now),
		buckets:   bucketsvm.New(s.api, now),
	}
	objectOptions := []objectsvm.Option{objectsvm.WithUploadConcurrency(c.prefs.IntWithFallback(settingsvm.PrefUploadConcurrency, settingsvm.DefaultUploadConcurrency))}
	if c.opts.Random != nil {
		objectOptions = append(objectOptions, objectsvm.WithRandom(c.opts.Random))
	}
	models.objects = objectsvm.New(s.api, now, objectOptions...)
	fileViewer, err := viewer.New(viewer.Dependencies{Windows: c.app, Source: s.api, Player: mpvPlayer{}, Random: rand.Reader})
	if err != nil {
		panic(err)
	}
	objectsScreen := objects.NewScreen(models.objects, nav, c.win, objects.Options{
		Context:     s.ctx,
		Notifier:    notifier,
		Clipboard:   c.clip,
		Now:         now,
		DownloadDir: c.downloadDir,
		Opener:      fileViewer,
	})
	models.settings = settingsvm.New(s.api, now, settingsvm.Session{
		Account:            c.opts.Engine,
		Preferences:        c.prefs,
		AppVersion:         c.opts.Version,
		DefaultDownloadDir: c.opts.DownloadDir,
	})
	s.models = models

	return []shell.Screen{
		dashboard.NewScreen(models.dashboard, nav, notifier, c.clip),
		buckets.NewScreenWithEnv(models.buckets, nav, buckets.Env{Window: window, Clipboard: c.clip}),
		objectsScreen,
		settingsui.NewScreen(models.settings, nav, notifier, c.clip, c.win),
	}
}

func (c *composition) downloadDir() string {
	return c.prefs.StringWithFallback(settingsvm.PrefDownloadDir, c.opts.DownloadDir)
}
