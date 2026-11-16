package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
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
		Build:     core.BuildInfo{Version: a.build.Version, Commit: a.build.Commit},
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
	serving, err := app.StartServing(ctx)
	if err != nil {
		a.fail(err)
		code := exitCodeFor(err)
		a.closeApp(app, cfg.Shutdown.TotalTimeout.Std())
		return code
	}
	app.Logger().Info("serve started", "component", "lifecycle", "data_dir", app.DataDir().Path())
	stopReload := a.reloadCertificateOnHangup(ctx, app, serving)
	<-ctx.Done()
	stopReload()
	return a.shutdown(app, serving, cfg.Shutdown.TotalTimeout.Std())
}

func (a *application) shutdown(app *core.App, serving *core.Serving, total time.Duration) int {
	closeCtx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()
	code := exitOK
	if err := serving.Close(closeCtx); err != nil {
		a.fail(err)
		code = exitFailure
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

func (a *application) reloadCertificateOnHangup(ctx context.Context, app *core.App, serving *core.Serving) func() {
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-hangup:
				if err := serving.ReloadCertificate(); err != nil {
					app.Logger().Error("admin certificate reload failed", "component", "admin", "error", err)
				}
			case <-stopped:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		signal.Stop(hangup)
		close(stopped)
		<-finished
	}
}
