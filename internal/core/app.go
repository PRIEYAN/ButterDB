package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/logging"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

type Dependencies struct {
	Config    config.Config
	Role      string
	LogOutput io.Writer
	Clock     clock.Clock
	FS        fsx.FS
	Build     BuildInfo
}

type App struct {
	cfg    config.Config
	logger *slog.Logger
	group  *lifecycle.Group
	clock  clock.Clock
	fsys   fsx.FS
	build  BuildInfo

	mu   sync.Mutex
	data *datadir.Dir
	lock *processlock.Lock
}

func New(deps Dependencies) (*App, error) {
	if deps.LogOutput == nil {
		return nil, fmt.Errorf("core: %w: log output", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("core: %w: clock", errs.ErrMissingDependency)
	}
	if deps.FS == nil {
		return nil, fmt.Errorf("core: %w: filesystem", errs.ErrMissingDependency)
	}
	if deps.Role == "" {
		return nil, fmt.Errorf("core: %w: role", errs.ErrMissingDependency)
	}
	app := &App{
		cfg:    deps.Config,
		logger: logging.New(deps.LogOutput, deps.Config.Log),
		group:  lifecycle.NewGroup(deps.Config.Shutdown.ComponentTimeout.Std()),
		clock:  deps.Clock,
		fsys:   deps.FS,
		build:  deps.Build,
	}
	app.group.Add("datadir", &dataDirComponent{app: app, fsys: deps.FS})
	app.group.Add("processlock", &lockComponent{app: app, role: deps.Role, clock: deps.Clock})
	return app, nil
}

func (a *App) Start(ctx context.Context) error {
	if err := a.group.Start(ctx); err != nil {
		return fmt.Errorf("core: start: %w", err)
	}
	return nil
}

func (a *App) Close(ctx context.Context) error {
	if err := a.group.Close(ctx); err != nil {
		return fmt.Errorf("core: close: %w", err)
	}
	return nil
}

func (a *App) Logger() *slog.Logger {
	return a.logger
}

func (a *App) DataDir() *datadir.Dir {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data
}
