package main

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/core"
	tgidentity "github.com/PRIEYAN/ButterDB/internal/identity"
)

func (a *application) runSetup(ctx context.Context, args []string) int {
	fs, path := a.newFlagSet("setup")
	roleName := fs.String("role", string(tgidentity.RolePrimary), "telegram account to link: primary or replica")
	registryChannel := fs.Int64("registry-channel", 0, "registry channel id to adopt without prompting")
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
	terminal := newConsoleTerminal(a.stdin, a.stdout)
	if code := a.linkAccount(ctx, command.session, terminal); code != exitOK {
		return code
	}
	if role != tgidentity.RolePrimary {
		return exitOK
	}
	return a.provisionRegistry(ctx, command.session, terminal, core.RegistryOptions{RegistryChannelID: *registryChannel})
}

func (a *application) linkAccount(ctx context.Context, session *core.TelegramSession, terminal core.Terminal) int {
	setup, err := core.NewSetup(core.SetupDependencies{
		Flow:     session.Identity(),
		Terminal: terminal,
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

func (a *application) provisionRegistry(ctx context.Context, session *core.TelegramSession, terminal core.Terminal, opts core.RegistryOptions) int {
	outcome, err := session.EnsureRegistry(ctx, terminal, opts)
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	switch {
	case outcome.Created:
		terminal.Say(fmt.Sprintf("Created registry channel %d for deployment %s.", outcome.ChannelID, outcome.DeploymentID))
	case outcome.Adopted:
		terminal.Say(fmt.Sprintf("Adopted registry channel %d for deployment %s. The next serve scans every channel from the start.", outcome.ChannelID, outcome.DeploymentID))
	default:
		terminal.Say(fmt.Sprintf("Registry channel %d is already set up.", outcome.ChannelID))
	}
	return exitOK
}
