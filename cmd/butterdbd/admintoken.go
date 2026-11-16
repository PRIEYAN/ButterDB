package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

var errServerUnreachable = errors.New("server is running but the control socket is unreachable")

func (a *application) runAdminToken(ctx context.Context, args []string) int {
	fs, path := a.newFlagSet("admin-token")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, findings, err := a.loadConfig(*path)
	if err != nil {
		a.printErrorFindings(findings)
		a.fail(err)
		return exitCodeFor(err)
	}
	kind, value, expiresAt, err := session.RequestAdminToken(ctx, cfg.Admin.ControlSocket)
	if err == nil {
		a.printAdminToken(kind, value, expiresAt)
		return exitOK
	}
	if !errors.Is(err, session.ErrControlSocketUnreachable) {
		a.fail(err)
		return exitFailure
	}
	return a.issueAdminTokenDirect(ctx, cfg)
}

func (a *application) issueAdminTokenDirect(ctx context.Context, cfg config.Config) int {
	app, err := core.New(core.Dependencies{
		Config:    cfg,
		Role:      "admin-token",
		LogOutput: a.stderr,
		Clock:     a.clock,
		FS:        a.fsys,
	})
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	if err := app.Start(ctx); err != nil {
		if errors.Is(err, processlock.ErrAlreadyRunning) {
			a.fail(errServerUnreachable)
			return exitFailure
		}
		a.fail(err)
		return exitCodeFor(err)
	}
	defer a.closeApp(app, cfg.Shutdown.TotalTimeout.Std())
	result, err := app.IssueAdminToken(ctx)
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	a.printAdminToken(result.Kind, result.Value, result.ExpiresAt)
	return exitOK
}

func (a *application) printAdminToken(kind, value string, expiresAt time.Time) {
	label := "pairing code"
	if kind == session.KindSetupToken {
		label = "setup token"
	}
	fmt.Fprintf(a.stdout, "%s: %s (expires %s)\n", label, value, expiresAt.UTC().Format(time.RFC3339))
}
