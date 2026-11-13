package main

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
)

func (a *application) runServe(ctx context.Context, args []string) int {
	fs, path := a.newFlagSet("serve")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, findings, err := a.loadConfig(*path)
	if err != nil {
		a.printErrorFindings(findings)
		a.fail(err)
		return exitCodeFor(err)
	}
	app, err := core.New(core.Dependencies{
		Config:    cfg,
		Role:      "serve",
		LogOutput: a.stdout,
		Clock:     a.clock,
		FS:        a.fsys,
	})
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	if err := app.Start(ctx); err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	app.Logger().Info("serve started", "component", "lifecycle", "data_dir", app.DataDir().Path())
	<-ctx.Done()
	return a.shutdown(app, cfg.Shutdown.TotalTimeout.Std())
}

func (a *application) shutdown(app *core.App, total time.Duration) int {
	closeCtx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()
	if err := app.Close(closeCtx); err != nil {
		a.fail(err)
		return exitFailure
	}
	app.Logger().Info("serve stopped", "component", "lifecycle")
	return exitOK
}
