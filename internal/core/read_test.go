package core

import (
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func completeReadDependencies(t *testing.T) ReadDependencies {
	t.Helper()
	clk := clock.NewFake(time.Unix(1763000000, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatal(err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: 1, FirstName: "Ada", Username: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Cache.Dir = t.TempDir()
	return ReadDependencies{
		Config: cfg, Database: &sqlitedb.DB{}, Sync: &Sync{}, Telegram: client, Origin: client, Ring: &keyring.Ring{},
		Files: fsx.OS{}, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: zeroReader{}, Logger: newDiscardLogger(),
	}
}

func TestNewReadRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*ReadDependencies)
	}{
		{name: "projection database", remove: func(d *ReadDependencies) { d.Database = nil }},
		{name: "sync", remove: func(d *ReadDependencies) { d.Sync = nil }},
		{name: "telegram", remove: func(d *ReadDependencies) { d.Telegram = nil }},
		{name: "range origin", remove: func(d *ReadDependencies) { d.Origin = nil }},
		{name: "server key", remove: func(d *ReadDependencies) { d.Ring = nil }},
		{name: "filesystem", remove: func(d *ReadDependencies) { d.Files = nil }},
		{name: "cache directory", remove: func(d *ReadDependencies) { d.Config.Cache.Dir = "" }},
		{name: "clock", remove: func(d *ReadDependencies) { d.Clock = nil }},
		{name: "sleeper", remove: func(d *ReadDependencies) { d.Sleeper = nil }},
		{name: "random", remove: func(d *ReadDependencies) { d.Random = nil }},
		{name: "logger", remove: func(d *ReadDependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := completeReadDependencies(t)
			tt.remove(&deps)
			if _, err := NewRead(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewRead = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestNewReadBuildsAndLifecycleRunsInOrder(t *testing.T) {
	read, err := NewRead(completeReadDependencies(t))
	if err != nil {
		t.Fatalf("NewRead: %v", err)
	}
	ctx := t.Context()
	if err := read.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := read.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
