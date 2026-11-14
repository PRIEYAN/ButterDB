package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	tgidentity "github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type telegramCommand struct {
	app     *core.App
	session *core.TelegramSession
	timeout time.Duration
}

func (a *application) openTelegram(ctx context.Context, cfg config.Config, subcommand string, role tgidentity.Role) (*telegramCommand, error) {
	app, err := core.New(core.Dependencies{
		Config:    cfg,
		Role:      subcommand,
		LogOutput: a.stderr,
		Clock:     a.clock,
		FS:        a.fsys,
	})
	if err != nil {
		return nil, err
	}
	if err := app.Start(ctx); err != nil {
		return nil, err
	}
	session, err := app.OpenTelegram(ctx, role)
	if err != nil {
		a.closeApp(app, cfg.Shutdown.TotalTimeout.Std())
		return nil, err
	}
	return &telegramCommand{app: app, session: session, timeout: cfg.Shutdown.TotalTimeout.Std()}, nil
}

func (a *application) closeApp(app *core.App, timeout time.Duration) {
	closeCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := app.Close(closeCtx); err != nil {
		a.fail(err)
	}
}

func (c *telegramCommand) close(a *application) {
	closeCtx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	if err := c.session.Close(closeCtx); err != nil {
		a.fail(err)
	}
	a.closeApp(c.app, c.timeout)
}

func parseRole(name string) (tgidentity.Role, error) {
	switch tgidentity.Role(name) {
	case tgidentity.RolePrimary, tgidentity.RoleReplica:
		return tgidentity.Role(name), nil
	}
	return "", fmt.Errorf("%w: --role must be primary or replica", errUsage)
}

func telegramFailure(err error) bool {
	return errors.Is(err, tgclient.ErrFloodWait) ||
		errors.Is(err, tgidentity.ErrTelegramUnavailable) ||
		errors.Is(err, tgidentity.ErrLoginFailed) ||
		errors.Is(err, tgclient.ErrNotAuthorized)
}
