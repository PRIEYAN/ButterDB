package core

import (
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func completeWriteDependencies(t *testing.T) WriteDependencies {
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
	return WriteDependencies{
		Data: &datadir.Dir{}, Control: &sqlitedb.DB{}, Database: &sqlitedb.DB{}, Projection: &Projection{}, Sync: &Sync{},
		Telegram: client, Sender: client, Pacer: &flow.SendLimiter{}, Ring: &keyring.Ring{}, ActorUserID: 1,
		FS: fsx.OS{}, Clock: clk, Sleeper: flow.SystemSleeper{}, Random: zeroReader{}, Logger: newDiscardLogger(),
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

func TestNewWriteRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*WriteDependencies)
	}{
		{name: "data directory", remove: func(d *WriteDependencies) { d.Data = nil }},
		{name: "control database", remove: func(d *WriteDependencies) { d.Control = nil }},
		{name: "projection database", remove: func(d *WriteDependencies) { d.Database = nil }},
		{name: "projection", remove: func(d *WriteDependencies) { d.Projection = nil }},
		{name: "sync", remove: func(d *WriteDependencies) { d.Sync = nil }},
		{name: "telegram", remove: func(d *WriteDependencies) { d.Telegram = nil }},
		{name: "sender", remove: func(d *WriteDependencies) { d.Sender = nil }},
		{name: "pacer", remove: func(d *WriteDependencies) { d.Pacer = nil }},
		{name: "server key", remove: func(d *WriteDependencies) { d.Ring = nil }},
		{name: "actor user id", remove: func(d *WriteDependencies) { d.ActorUserID = 0 }},
		{name: "filesystem", remove: func(d *WriteDependencies) { d.FS = nil }},
		{name: "clock", remove: func(d *WriteDependencies) { d.Clock = nil }},
		{name: "sleeper", remove: func(d *WriteDependencies) { d.Sleeper = nil }},
		{name: "random", remove: func(d *WriteDependencies) { d.Random = nil }},
		{name: "logger", remove: func(d *WriteDependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := completeWriteDependencies(t)
			tt.remove(&deps)
			if _, err := NewWrite(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewWrite = %v, want ErrMissingDependency", err)
			}
		})
	}
}
