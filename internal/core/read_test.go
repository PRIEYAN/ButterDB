package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
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

type countingOrigin struct {
	tgclient.RangeReader
	calls int
}

func (o *countingOrigin) ReadDocumentRange(_ context.Context, _ tgclient.DocumentRef, _ int64, dst []byte) (int, error) {
	o.calls++
	for i := range dst {
		dst[i] = byte(i)
	}
	return len(dst), nil
}

func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

func TestReadPathWithCacheOffKeepsNoBlocks(t *testing.T) {
	cases := []struct {
		name          string
		blocksMax     config.ByteSize
		wantFiles     bool
		wantOriginHit int
	}{
		{name: "default config", blocksMax: config.Default().Cache.BlocksMaxBytes, wantFiles: false, wantOriginHit: 2},
		{name: "opt in with a positive size", blocksMax: 1 << 20, wantFiles: true, wantOriginHit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := completeReadDependencies(t)
			origin := &countingOrigin{RangeReader: deps.Origin}
			deps.Origin = origin
			deps.Config.Cache.BlocksMaxBytes = tc.blocksMax
			cacheRoot := filepath.Join(t.TempDir(), "cache")
			deps.Config.Cache.Dir = cacheRoot
			read, err := NewRead(deps)
			if err != nil {
				t.Fatalf("NewRead: %v", err)
			}
			ctx := t.Context()
			if err := read.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			ref := tgclient.DocumentRef{Peer: tgclient.InputPeer{ChannelID: 7}, MsgID: 9}
			for range 2 {
				dst := make([]byte, 4096)
				if n, err := read.Cache.ReadDocumentRange(ctx, ref, 0, dst); err != nil || n != len(dst) {
					t.Fatalf("ReadDocumentRange = %d, %v", n, err)
				}
			}
			if err := read.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if origin.calls != tc.wantOriginHit {
				t.Fatalf("origin reads = %d, want %d", origin.calls, tc.wantOriginHit)
			}
			_, statErr := os.Stat(cacheRoot)
			if tc.wantFiles {
				if len(filesUnder(t, cacheRoot)) == 0 {
					t.Fatal("enabled cache wrote no block file")
				}
				return
			}
			if !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("cache directory exists with the cache off: stat error %v, files %v", statErr, filesUnder(t, cacheRoot))
			}
			if got := read.Cache.Stats().LimitBytes; got != 0 {
				t.Fatalf("limit bytes = %d, want 0", got)
			}
		})
	}
}
