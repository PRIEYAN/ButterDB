//go:build linux

package nativeplayer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const mpvProbeTimeout = 3 * time.Second

var linuxMPVVersions sync.Map

func findLinuxMPV() (string, mpvVersion, error) {
	if override := os.Getenv("BUTTERDB_MPV_BIN"); override != "" {
		if st, err := os.Stat(override); err == nil && !st.IsDir() {
			version, err := linuxMPVVersion(override)
			return override, version, err
		}
		return "", mpvVersion{}, fmt.Errorf("BUTTERDB_MPV_BIN is not executable")
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, candidate := range []string{
			filepath.Join(dir, "media", "mpv"),
			filepath.Join(dir, "mpv"),
			filepath.Join(dir, "..", "lib", "butterdb", "mpv"),
		} {
			if st, err := os.Stat(candidate); err != nil || st.IsDir() {
				continue
			}
			version, err := linuxMPVVersion(candidate)
			if err != nil {
				linuxNativeLogf("bundled mpv unusable, falling back: path=%s err=%v", candidate, err)
				continue
			}
			return candidate, version, nil
		}
	}

	if path, err := exec.LookPath("mpv"); err == nil {
		version, err := linuxMPVVersion(path)
		if err != nil {
			return "", mpvVersion{}, fmt.Errorf("native player: system mpv cannot run: %w", err)
		}
		return path, version, nil
	}
	return "", mpvVersion{}, fmt.Errorf("native player: mpv executable not found")
}

func linuxMPVVersion(path string) (mpvVersion, error) {
	if cached, ok := linuxMPVVersions.Load(path); ok {
		return cached.(mpvVersion), nil
	}
	version, err := probeLinuxMPV(path)
	if err != nil {
		return mpvVersion{}, err
	}
	linuxMPVVersions.Store(path, version)
	return version, nil
}

func probeLinuxMPV(path string) (mpvVersion, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mpvProbeTimeout)
	defer cancel()

	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--no-config", "--version")
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		if tail := strings.Join(strings.Fields(output.String()), " "); tail != "" {
			return mpvVersion{}, fmt.Errorf("%w: %s", err, tail)
		}
		return mpvVersion{}, err
	}
	version := parseMPVVersion(output.String())
	if !version.known() {
		linuxNativeLogf("mpv version unknown, using only the options every release accepts: path=%s", path)
	}
	return version, nil
}

func linuxMPVArgs(version mpvVersion, ipcPath string, windowID uintptr, opts Options) []string {
	args := []string{
		"--no-config",
		"--terminal=yes",
		"--msg-level=all=error",
		"--ytdl=no",
		"--cache=yes",
		"--demuxer-readahead-secs=20",
		"--demuxer-max-bytes=67108864",
		"--demuxer-max-back-bytes=33554432",
		"--cache-pause-wait=3",
		"--keepaspect=yes",
		"--force-window=immediate",
		"--input-terminal=no",
		"--idle=yes",
		"--keep-open=yes",
		"--input-ipc-server=" + ipcPath,
	}
	args = append(args, protectedMPVArgs(opts.Protected)...)
	if opts.AudioOnly {
		return append(args, "--vid=no", "--force-window=no", "--audio-display=no", "--no-input-default-bindings")
	}
	if version.atLeast(0, 34) {
		args = append(args, "--hwdec=auto-safe")
	}
	if windowID != 0 {
		args = append(args,
			"--keepaspect-window=no",
			"--osc=no",
			"--osd-bar=no",
			"--osd-level=0",
			"--cursor-autohide=no",
			"--no-input-default-bindings",
			"--input-vo-keyboard=no",
			fmt.Sprintf("--wid=%d", windowID),
		)
		if version.atLeast(0, 36) {
			args = append(args, "--auto-window-resize=no")
		}
		return args
	}
	bindings := "--input-default-bindings=yes"
	if opts.Protected {
		bindings = "--input-default-bindings=no"
	}
	return append(args,
		standaloneTitle(opts.Title),
		"--osc=yes",
		"--osd-bar=yes",
		"--osd-level=1",
		bindings,
		"--input-vo-keyboard=yes",
	)
}

func standaloneTitle(name string) string {
	if strings.TrimSpace(name) == "" {
		return "--title=${media-title} — butterDB"
	}
	return "--title=" + strings.ReplaceAll(name, "$", "$$") + " — butterDB"
}
