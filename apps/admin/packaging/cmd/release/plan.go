package main

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	appSlug          = "butterdb-admin"
	fyneToolPackage  = "fyne.io/tools/cmd/fyne"
	fyneCrossPackage = "github.com/fyne-io/fyne-cross@v1.6.3"
	checksumsFile    = "SHA256SUMS"
	defaultDistDir   = "dist"
	crossOutputDir   = "fyne-cross"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type target struct {
	OS   string
	Arch string
}

type command struct {
	Dir  string
	Env  []string
	Name string
	Args []string
}

func (c command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	for _, arg := range c.Args {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if arg == "" || strings.ContainsAny(arg, " \t'\"") {
		return fmt.Sprintf("%q", arg)
	}
	return arg
}

func artifactName(version, goos, goarch string) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("release: version %q is not x.y.z", version)
	}
	extension, err := archiveExtension(goos)
	if err != nil {
		return "", err
	}
	if err := checkArchitecture(goos, goarch); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", appSlug, version, goos, goarch, extension), nil
}

func archiveExtension(goos string) (string, error) {
	switch goos {
	case "linux":
		return ".tar.xz", nil
	case "windows", "darwin":
		return ".zip", nil
	}
	return "", fmt.Errorf("release: unsupported os %q", goos)
}

func checkArchitecture(goos, goarch string) error {
	switch goarch {
	case "amd64":
		return nil
	case "arm64":
		if goos == "windows" {
			return fmt.Errorf("release: windows arm64 is not a release target")
		}
		return nil
	}
	return fmt.Errorf("release: unsupported architecture %q", goarch)
}

func crossArchitectures(goos string) ([]string, error) {
	switch goos {
	case "linux":
		return []string{"amd64", "arm64"}, nil
	case "windows":
		return []string{"amd64"}, nil
	}
	return nil, fmt.Errorf("release: cross builds for %s are not supported, build natively on that OS", goos)
}

func planTargets(cfg Config) ([]target, error) {
	if cfg.Cross {
		return planCrossTargets(cfg)
	}
	if cfg.OS != cfg.HostOS {
		return nil, fmt.Errorf("release: %s packages need a %s host or -cross", cfg.OS, cfg.OS)
	}
	arch := cfg.Arch
	if arch == "" {
		arch = cfg.HostArch
	}
	if arch != cfg.HostArch {
		return nil, fmt.Errorf("release: native builds target the host architecture %s, use -cross for %s", cfg.HostArch, arch)
	}
	return []target{{OS: cfg.OS, Arch: arch}}, nil
}

func planCrossTargets(cfg Config) ([]target, error) {
	arches, err := crossArchitectures(cfg.OS)
	if err != nil {
		return nil, err
	}
	if cfg.Arch != "" {
		arches = []string{cfg.Arch}
	}
	targets := make([]target, 0, len(arches))
	for _, arch := range arches {
		if err := checkArchitecture(cfg.OS, arch); err != nil {
			return nil, err
		}
		targets = append(targets, target{OS: cfg.OS, Arch: arch})
	}
	return targets, nil
}

func nativeCommand(appDir string, version string, tgt target) command {
	return command{
		Env:  []string{"CGO_ENABLED=1"},
		Name: "go",
		Args: []string{
			"-C", appDir, "run", fyneToolPackage, "package",
			"-os", tgt.OS, "-release", "--app-version", version,
		},
	}
}

func crossCommand(appDir string, version string, meta appMetadata, goos string, arches []string) command {
	return command{
		Name: "go",
		Args: []string{
			"-C", appDir, "run", fyneCrossPackage, goos,
			"-arch=" + strings.Join(arches, ","),
			"-app-id", meta.ID,
			"-app-version", version,
		},
	}
}
