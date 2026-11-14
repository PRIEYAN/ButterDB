package main

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/core"
	tgidentity "github.com/PRIEYAN/ButterDB/internal/identity"
)

func (a *application) runSetup(ctx context.Context, args []string) int {
	fs, path := a.newFlagSet("setup")
	roleName := fs.String("role", string(tgidentity.RolePrimary), "telegram account to link: primary or replica")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	role, err := parseRole(*roleName)
	if err != nil {
		a.fail(err)
		return exitUsage
	}
	cfg, findings, err := a.loadConfig(*path)
	if err != nil {
		a.printErrorFindings(findings)
		a.fail(err)
		return exitCodeFor(err)
	}
	command, err := a.openTelegram(ctx, cfg, "setup", role)
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	defer command.close(a)
	return a.linkAccount(ctx, command.session)
}

func (a *application) linkAccount(ctx context.Context, session *core.TelegramSession) int {
	setup, err := core.NewSetup(core.SetupDependencies{
		Flow:     session.Identity(),
		Terminal: newConsoleTerminal(a.stdin, a.stdout),
	})
	if err != nil {
		a.fail(err)
		return exitFailure
	}
	if _, err := setup.Run(ctx); err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	return exitOK
}
