package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// The linker sets these through -ldflags -X, which can only write package-level string variables.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app := newApplication(os.Stdout, os.Stderr, os.Environ(), buildInfo{Version: version, Commit: commit})
	code := app.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
