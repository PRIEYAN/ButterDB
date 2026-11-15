package main

import (
	"context"
	"errors"
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
	if err := app.VerifyLocalState(ctx); err != nil {
		a.fail(err)
		code := exitCodeFor(err)
		a.closeApp(app, cfg.Shutdown.TotalTimeout.Std())
		return code
	}
	serving, err := a.startServing(ctx, app)
	if err != nil {
		a.fail(err)
		code := exitCodeFor(err)
		a.closeApp(app, cfg.Shutdown.TotalTimeout.Std())
		return code
	}
	app.Logger().Info("serve started", "component", "lifecycle", "data_dir", app.DataDir().Path())
	<-ctx.Done()
	return a.shutdown(app, serving, cfg.Shutdown.TotalTimeout.Std())
}

func (a *application) startServing(ctx context.Context, app *core.App) (*core.Serving, error) {
	serving, err := app.StartServing(ctx)
	if errors.Is(err, core.ErrNotLinked) {
		app.Logger().Info("s3 listener not started: deployment is not linked", "component", "lifecycle")
		return nil, nil
	}
	return serving, err
}

func (a *application) shutdown(app *core.App, serving *core.Serving, total time.Duration) int {
	closeCtx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()
	code := exitOK
	if serving != nil {
		if err := serving.Close(closeCtx); err != nil {
			a.fail(err)
			code = exitFailure
		}
	}
	if err := app.Close(closeCtx); err != nil {
		a.fail(err)
		return exitFailure
	}
	if code == exitOK {
		app.Logger().Info("serve stopped", "component", "lifecycle")
	}
	return code
}
