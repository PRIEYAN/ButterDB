package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const (
	defaultConfigPath = "/etc/butterdb/butterdb.toml"
	configPathEnv     = "BUTTERDB_CONFIG"
	programName       = "butterdbd"
)

type buildInfo struct {
	Version string
	Commit  string
}

type application struct {
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
	environ []string
	build   buildInfo
	clock   clock.Clock
	fsys    fsx.FS
	files   hostFiles
	volumes hostVolumes
	ids     identity
}

func newApplication(stdout, stderr io.Writer, environ []string, build buildInfo) *application {
	return &application{
		stdout:  stdout,
		stderr:  stderr,
		stdin:   os.Stdin,
		environ: environ,
		build:   build,
		clock:   clock.System{},
		fsys:    fsx.OS{},
		files:   osFiles{},
		volumes: osVolumes{},
		ids:     processIdentity(),
	}
}

func (a *application) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.printUsage(a.stderr)
		return exitUsage
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		a.printUsage(a.stdout)
		return exitOK
	case "version":
		return a.runVersion(rest)
	case "check-config":
		return a.runCheckConfig(rest)
	case "serve":
		return a.runServe(ctx, rest)
	case "setup":
		return a.runSetup(ctx, rest)
	case "tgprobe":
		return a.runTGProbe(ctx, rest)
	case "admin-token":
		return a.runAdminToken(ctx, rest)
	case "rebuild", "snapshot", "restore":
		a.fail(fmt.Errorf("subcommand %q is not available in this build", name))
		return exitFailure
	default:
		a.fail(fmt.Errorf("%w: unknown subcommand %q", errUsage, name))
		a.printUsage(a.stderr)
		return exitUsage
	}
}

func (a *application) fail(err error) {
	fmt.Fprintf(a.stderr, "%s: %s\n", programName, terminalSafe(err.Error()))
}

func (a *application) printUsage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s <subcommand> [--config <path>] [flags]\n", programName)
	fmt.Fprintln(w, "subcommands: serve, setup, admin-token, rebuild, snapshot, restore, check-config, version, tgprobe")
}

func (a *application) newFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(programName+" "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	path := fs.String("config", "", "path of the configuration file")
	return fs, path
}

func (a *application) loadConfig(flagPath string) (config.Config, []config.Finding, error) {
	opts := config.LoadOptions{Path: defaultConfigPath, Environ: a.environ}
	switch {
	case flagPath != "":
		opts.Path, opts.PathExplicit = flagPath, true
	default:
		if fromEnv := lookupEnv(a.environ, configPathEnv); fromEnv != "" {
			opts.Path, opts.PathExplicit = fromEnv, true
		}
	}
	return config.Load(opts)
}

func lookupEnv(environ []string, name string) string {
	prefix := name + "="
	value := ""
	for _, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

func terminalSafe(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, text)
}
