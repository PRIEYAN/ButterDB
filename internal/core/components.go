package core

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

type dataDirComponent struct {
	app  *App
	fsys fsx.FS
}

func (c *dataDirComponent) Start(_ context.Context) error {
	dir, err := datadir.Open(c.fsys, c.app.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("core: data directory: %w", err)
	}
	c.app.mu.Lock()
	c.app.data = dir
	c.app.mu.Unlock()
	return nil
}

func (c *dataDirComponent) Close(_ context.Context) error {
	return nil
}

type lockComponent struct {
	app   *App
	role  string
	clock clock.Clock
}

func (c *lockComponent) Start(_ context.Context) error {
	dir := c.app.DataDir()
	held, err := processlock.Acquire(dir.LockPath(), c.role, c.clock)
	if err != nil {
		return fmt.Errorf("core: process lock: %w", err)
	}
	c.app.mu.Lock()
	c.app.lock = held
	c.app.mu.Unlock()
	return nil
}

func (c *lockComponent) Close(_ context.Context) error {
	c.app.mu.Lock()
	held := c.app.lock
	c.app.lock = nil
	c.app.mu.Unlock()
	if held == nil {
		return nil
	}
	if err := held.Release(); err != nil {
		return fmt.Errorf("core: release process lock: %w", err)
	}
	return nil
}
