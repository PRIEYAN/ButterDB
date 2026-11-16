package main

import (
	"flag"
	"fmt"
	"io"
)

type Config struct {
	AppDir   string
	DistDir  string
	Version  string
	OS       string
	Arch     string
	Cross    bool
	DryRun   bool
	HostOS   string
	HostArch string
}

func parseConfig(args []string, hostOS string, hostArch string, usage io.Writer) (Config, error) {
	cfg := Config{HostOS: hostOS, HostArch: hostArch}
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(usage)
	flags.StringVar(&cfg.AppDir, "dir", ".", "admin app module directory")
	flags.StringVar(&cfg.DistDir, "out", defaultDistDir, "output directory, relative to -dir")
	flags.StringVar(&cfg.Version, "version", "", "release version x.y.z, default is the latest v* git tag, then FyneApp.toml")
	flags.StringVar(&cfg.OS, "os", hostOS, "target os: linux, windows or darwin")
	flags.StringVar(&cfg.Arch, "arch", "", "target architecture, default is the host (native) or every release architecture (-cross)")
	flags.BoolVar(&cfg.Cross, "cross", false, "build with fyne-cross inside a container")
	flags.BoolVar(&cfg.DryRun, "dry-run", false, "print the plan and run nothing")
	if err := flags.Parse(args); err != nil {
		return Config{}, fmt.Errorf("release: %w", err)
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("release: unexpected argument %q", flags.Arg(0))
	}
	return cfg, nil
}
