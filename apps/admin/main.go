package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/app"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

const (
	version      = "0.1.0"
	closeTimeout = 30 * time.Second
	logFileName  = "app.log"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "butterdb-admin:", err)
		os.Exit(1)
	}
}

func run() error {
	logOutput, closeLog := openLog()
	defer closeLog()
	host, err := embedded.Open(embedded.Options{DataHome: "", LogOutput: logOutput, Version: version})
	if errors.Is(err, embedded.ErrAlreadyRunning) {
		fmt.Fprintln(os.Stderr, "butterDB is already open")
		closeLog()
		os.Exit(1)
	}
	if err != nil {
		return err
	}
	runErr := app.Run(app.Options{
		Version:     version,
		Now:         time.Now,
		Engine:      host,
		Random:      rand.Reader,
		DownloadDir: downloadDir(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	_ = host.CloseProject(ctx)
	if err := host.Close(ctx); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

func dataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "butterdb")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "butterdb")
}

func openLog() (io.Writer, func()) {
	dir := dataHome()
	if dir == "" {
		return io.Discard, func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return io.Discard, func() {}
	}
	file, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return io.Discard, func() {}
	}
	return file, func() { _ = file.Close() }
}

func downloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads")
}
