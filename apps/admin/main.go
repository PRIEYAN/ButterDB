package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/app"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/credstore"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/profilestore"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "butterdb-admin:", err)
		os.Exit(1)
	}
}

func run() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("find config directory: %w", err)
	}
	store, err := profilestore.New(profilestore.PathIn(configDir))
	if err != nil {
		return err
	}
	files, err := credstore.NewFile(credstore.TokenDirIn(configDir))
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	return app.Run(app.Options{
		Version:     version,
		Now:         time.Now,
		Dialer:      app.NewDialer(),
		Tokens:      app.ProbeKeychain(credstore.NewKeychain()),
		Store:       store,
		Files:       files,
		Memory:      credstore.NewMemory(),
		Random:      rand.Reader,
		DeviceName:  hostname,
		DownloadDir: downloadDir(),
	})
}

func downloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads")
}
