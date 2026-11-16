package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Dependencies struct {
	Runner commandRunner
	Out    io.Writer
}

type Releaser struct {
	runner commandRunner
	out    io.Writer
}

func NewReleaser(deps Dependencies) (*Releaser, error) {
	if deps.Runner == nil {
		return nil, errors.New("release: missing dependency: runner")
	}
	if deps.Out == nil {
		return nil, errors.New("release: missing dependency: out")
	}
	return &Releaser{runner: deps.Runner, out: deps.Out}, nil
}

type job struct {
	appDir  string
	distDir string
	version string
	meta    appMetadata
	targets []target
	cfg     Config
}

func (r *Releaser) Run(ctx context.Context, cfg Config) error {
	work, err := r.prepare(ctx, cfg)
	if err != nil {
		return err
	}
	if cfg.DryRun {
		return r.describe(work)
	}
	return r.build(ctx, work)
}

func (r *Releaser) prepare(ctx context.Context, cfg Config) (job, error) {
	appDir, err := filepath.Abs(cfg.AppDir)
	if err != nil {
		return job{}, fmt.Errorf("release: resolve app dir: %w", err)
	}
	meta, err := loadMetadata(appDir)
	if err != nil {
		return job{}, err
	}
	version, err := r.resolveVersion(ctx, cfg, appDir, meta)
	if err != nil {
		return job{}, err
	}
	targets, err := planTargets(cfg)
	if err != nil {
		return job{}, err
	}
	distDir := cfg.DistDir
	if !filepath.IsAbs(distDir) {
		distDir = filepath.Join(appDir, distDir)
	}
	return job{appDir: appDir, distDir: distDir, version: version, meta: meta, targets: targets, cfg: cfg}, nil
}

func (r *Releaser) resolveVersion(ctx context.Context, cfg Config, appDir string, meta appMetadata) (string, error) {
	if cfg.Version != "" {
		if !versionPattern.MatchString(cfg.Version) {
			return "", fmt.Errorf("release: version %q is not x.y.z", cfg.Version)
		}
		return cfg.Version, nil
	}
	described, err := r.runner.Output(ctx, command{
		Dir:  appDir,
		Name: "git",
		Args: []string{"describe", "--tags", "--abbrev=0", "--match", "v*"},
	})
	if tag := strings.TrimPrefix(described, "v"); err == nil && versionPattern.MatchString(tag) {
		return tag, nil
	}
	if versionPattern.MatchString(meta.Version) {
		return meta.Version, nil
	}
	return "", fmt.Errorf("release: no x.y.z version from -version, git tags or %s", fyneMetadataFile)
}

func (r *Releaser) commands(work job) []command {
	if work.cfg.Cross {
		arches := make([]string, 0, len(work.targets))
		for _, tgt := range work.targets {
			arches = append(arches, tgt.Arch)
		}
		return []command{crossCommand(work.appDir, work.version, work.meta, work.cfg.OS, arches)}
	}
	return []command{nativeCommand(work.appDir, work.version, work.targets[0])}
}

func (r *Releaser) describe(work job) error {
	for _, cmd := range r.commands(work) {
		fmt.Fprintf(r.out, "run: %s\n", cmd)
	}
	for _, tgt := range work.targets {
		name, err := artifactName(work.version, tgt.OS, tgt.Arch)
		if err != nil {
			return err
		}
		fmt.Fprintf(r.out, "write: %s\n", filepath.Join(work.distDir, name))
	}
	fmt.Fprintf(r.out, "write: %s\n", filepath.Join(work.distDir, checksumsFile))
	fmt.Fprintf(r.out, "keep: %s unchanged\n", filepath.Join(work.appDir, fyneMetadataFile))
	return nil
}

func (r *Releaser) build(ctx context.Context, work job) (err error) {
	restore, err := snapshotFile(filepath.Join(work.appDir, fyneMetadataFile))
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, restore())
	}()
	if err := os.MkdirAll(work.distDir, 0o755); err != nil {
		return fmt.Errorf("release: create dist dir: %w", err)
	}
	if work.cfg.Cross {
		return r.buildCross(ctx, work)
	}
	return r.buildNative(ctx, work)
}

func (r *Releaser) buildNative(ctx context.Context, work job) error {
	tgt := work.targets[0]
	name, err := artifactName(work.version, tgt.OS, tgt.Arch)
	if err != nil {
		return err
	}
	produced := filepath.Join(work.appDir, work.meta.Name+nativeSuffix(tgt.OS))
	if err := refuseExisting(produced); err != nil {
		return err
	}
	defer os.RemoveAll(produced)
	if err := r.runner.Run(ctx, r.commands(work)[0]); err != nil {
		return err
	}
	if err := packNative(tgt.OS, filepath.Join(work.distDir, name), produced); err != nil {
		return err
	}
	return r.finish(work.distDir, name)
}

func nativeSuffix(goos string) string {
	switch goos {
	case "darwin":
		return ".app"
	case "windows":
		return ".exe"
	}
	return ".tar.xz"
}

func packNative(goos string, destination string, produced string) error {
	switch goos {
	case "darwin":
		return zipDirectory(destination, produced)
	case "windows":
		return zipSingleFile(destination, produced, appSlug+".exe")
	}
	return moveFile(destination, produced)
}

func (r *Releaser) buildCross(ctx context.Context, work job) error {
	crossDir := filepath.Join(work.appDir, crossOutputDir)
	if err := refuseExisting(crossDir); err != nil {
		return err
	}
	defer os.RemoveAll(crossDir)
	if err := r.runner.Run(ctx, r.commands(work)[0]); err != nil {
		return err
	}
	for _, tgt := range work.targets {
		name, err := artifactName(work.version, tgt.OS, tgt.Arch)
		if err != nil {
			return err
		}
		produced, err := crossArchive(crossDir, tgt)
		if err != nil {
			return err
		}
		if err := moveFile(filepath.Join(work.distDir, name), produced); err != nil {
			return err
		}
		if err := r.finish(work.distDir, name); err != nil {
			return err
		}
	}
	return nil
}

func crossArchive(crossDir string, tgt target) (string, error) {
	extension, err := archiveExtension(tgt.OS)
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(crossDir, "dist", tgt.OS+"-"+tgt.Arch, "*"+extension))
	if err != nil {
		return "", fmt.Errorf("release: find %s/%s archive: %w", tgt.OS, tgt.Arch, err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("release: expected one %s archive for %s/%s, found %d", extension, tgt.OS, tgt.Arch, len(matches))
	}
	return matches[0], nil
}

func (r *Releaser) finish(distDir string, name string) error {
	if err := recordChecksum(distDir, name); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "wrote: %s\n", filepath.Join(distDir, name))
	return nil
}

func refuseExisting(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("release: %s already exists, remove it first", filepath.Base(path))
	}
	return nil
}

func snapshotFile(path string) (func() error, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("release: read %s: %w", filepath.Base(path), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("release: stat %s: %w", filepath.Base(path), err)
	}
	return func() error {
		current, err := os.ReadFile(path)
		if err == nil && string(current) == string(original) {
			return nil
		}
		if err := os.WriteFile(path, original, info.Mode().Perm()); err != nil {
			return fmt.Errorf("release: restore %s: %w", filepath.Base(path), err)
		}
		return nil
	}, nil
}
