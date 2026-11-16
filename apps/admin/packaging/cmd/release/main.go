package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseConfig(args, runtime.GOOS, runtime.GOARCH, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	releaser, err := NewReleaser(Dependencies{Runner: execRunner{stdout: stdout, stderr: stderr}, Out: stdout})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := releaser.Run(ctx, cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
