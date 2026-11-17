package app

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/viewer"
)

type mpvPlayer struct {
	executable string
}

func (p mpvPlayer) Play(ctx context.Context, window uintptr, source string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if window == 0 {
		return fmt.Errorf("mpv: missing native window")
	}
	binary := p.executable
	if binary == "" {
		binary = "mpv"
	}
	binary, err := exec.LookPath(binary)
	if err != nil {
		return viewer.ErrPlayerUnavailable
	}
	args := []string{
		"--no-config", "--load-scripts=no", "--ytdl=no", "--terminal=no",
		"--wid=" + strconv.FormatUint(uint64(window), 10), "--force-window=immediate",
		"--osc=yes", "--script-opts=osc-visibility=always", "--keep-open=yes",
		"--input-default-bindings=yes", "--input-vo-keyboard=yes",
		"--cache=yes", "--cache-on-disk=no", "--demuxer-max-bytes=32MiB",
		"--demuxer-max-back-bytes=8MiB", "--demuxer-readahead-secs=10",
		"--sub-auto=no", "--audio-file-auto=no", "--title=butterDB viewer",
	}
	if runtime.GOOS == "linux" {
		args = append(args, "--vo=gpu", "--gpu-context=x11egl")
	}
	args = append(args, "--", source)
	err = exec.CommandContext(ctx, binary, args...).Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("mpv: playback failed: %w", err)
	}
	return nil
}
