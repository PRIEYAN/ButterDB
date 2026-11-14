package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/core"
	tgidentity "github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	probeReportMode    os.FileMode = 0o600
	probeConfirmAnswer             = "yes"
)

var errProbeDeclined = errors.New("tgprobe: not confirmed")

func (a *application) runTGProbe(ctx context.Context, args []string) int {
	fs, path := a.newFlagSet("tgprobe")
	out := fs.String("out", "", "path of the JSON report (default tgprobe-YYYYMMDD.json)")
	stepList := fs.String("steps", "", "comma separated steps: control,upload,download,history,delete")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	steps := core.ParseProbeSteps(*stepList)
	if err := core.ValidateProbeSteps(steps); err != nil {
		a.fail(fmt.Errorf("%w: %s", errUsage, err.Error()))
		return exitUsage
	}
	cfg, findings, err := a.loadConfig(*path)
	if err != nil {
		a.printErrorFindings(findings)
		a.fail(err)
		return exitCodeFor(err)
	}
	command, err := a.openTelegram(ctx, cfg, "tgprobe", tgidentity.RolePrimary)
	if err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	defer command.close(a)
	if err := a.probeAccount(ctx, command.session, steps, *out, *yes); err != nil {
		a.fail(err)
		return exitCodeFor(err)
	}
	return exitOK
}

func (a *application) probeAccount(ctx context.Context, session *core.TelegramSession, steps []string, out string, assumeYes bool) error {
	snap, err := session.Identity().Status(ctx)
	if err != nil {
		return err
	}
	if snap.State != tgidentity.StateLinked || snap.Account == nil {
		return fmt.Errorf("tgprobe: no linked telegram account; run %s setup first: %w", programName, tgclient.ErrNotAuthorized)
	}
	terminal := newConsoleTerminal(a.stdin, a.stdout)
	if !assumeYes {
		if err := a.confirmProbe(terminal, snap, steps); err != nil {
			return err
		}
	}
	probe, err := session.NewProbe(ctx, fmt.Sprintf("tgprobe-%d", a.clock.Now().UnixNano()))
	if err != nil {
		return err
	}
	opts := session.ProbeOptions(a.build.Version)
	opts.Steps = steps
	report, runErr := probe.Run(ctx, tgclient.Account{UserID: snap.Account.UserID}, opts)
	writeErr := a.writeProbeReport(out, report)
	return errors.Join(runErr, writeErr)
}

func (a *application) confirmProbe(terminal *consoleTerminal, snap tgidentity.Snapshot, steps []string) error {
	terminal.Say(fmt.Sprintf("tgprobe will run [%s] against Telegram user %d (%s).", strings.Join(steps, ", "), snap.Account.UserID, snap.Account.PhoneMasked))
	terminal.Say("It deliberately drives the account into its first flood wait. Use a dedicated account only.")
	answer, err := terminal.Prompt(fmt.Sprintf("type %q to continue", probeConfirmAnswer))
	if err != nil {
		return fmt.Errorf("tgprobe: %w", err)
	}
	if strings.TrimSpace(answer) != probeConfirmAnswer {
		return errProbeDeclined
	}
	return nil
}

func (a *application) writeProbeReport(out string, report core.ProbeReport) error {
	if out == "" {
		out = "tgprobe-" + a.clock.Now().UTC().Format("20060102") + ".json"
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("tgprobe: encode report: %w", err)
	}
	if err := os.WriteFile(out, append(encoded, '\n'), probeReportMode); err != nil {
		return fmt.Errorf("tgprobe: write report: %w", err)
	}
	fmt.Fprintf(a.stdout, "tgprobe report written to %s\n", out)
	return nil
}
