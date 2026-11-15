package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

type fakeRegistryState struct{}

func (*fakeRegistryState) LoadRegistryState(context.Context) (buckets.RegistryState, bool, error) {
	return buckets.RegistryState{}, false, nil
}

func (*fakeRegistryState) SaveRegistryState(context.Context, buckets.RegistryState) error {
	return nil
}

func completeRuntimeDependencies(t *testing.T) RuntimeDependencies {
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
	return RuntimeDependencies{
		Config: config.Default(), Data: &datadir.Dir{}, Control: &sqlitedb.DB{}, Database: &sqlitedb.DB{}, Ring: &keyring.Ring{},
		State: &fakeRegistryState{}, Telegram: client, Sender: client, Prepaced: client, Origin: client, Signals: conn.NewActivity(0),
		Pacer: &flow.SendLimiter{}, ActorUserID: 1, DeploymentID: "deployment", FS: fsx.OS{}, Files: fsx.OS{},
		Clock: clk, Sleeper: flow.SystemSleeper{}, Random: zeroReader{}, Logger: newDiscardLogger(),
	}
}

func TestNewRuntimeRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*RuntimeDependencies)
	}{
		{name: "data directory", remove: func(d *RuntimeDependencies) { d.Data = nil }},
		{name: "control database", remove: func(d *RuntimeDependencies) { d.Control = nil }},
		{name: "projection database", remove: func(d *RuntimeDependencies) { d.Database = nil }},
		{name: "server key", remove: func(d *RuntimeDependencies) { d.Ring = nil }},
		{name: "registry state", remove: func(d *RuntimeDependencies) { d.State = nil }},
		{name: "telegram", remove: func(d *RuntimeDependencies) { d.Telegram = nil }},
		{name: "sender", remove: func(d *RuntimeDependencies) { d.Sender = nil }},
		{name: "pre-paced sender", remove: func(d *RuntimeDependencies) { d.Prepaced = nil }},
		{name: "range origin", remove: func(d *RuntimeDependencies) { d.Origin = nil }},
		{name: "signals", remove: func(d *RuntimeDependencies) { d.Signals = nil }},
		{name: "pacer", remove: func(d *RuntimeDependencies) { d.Pacer = nil }},
		{name: "deployment id", remove: func(d *RuntimeDependencies) { d.DeploymentID = "" }},
		{name: "filesystem", remove: func(d *RuntimeDependencies) { d.FS = nil }},
		{name: "cache files", remove: func(d *RuntimeDependencies) { d.Files = nil }},
		{name: "clock", remove: func(d *RuntimeDependencies) { d.Clock = nil }},
		{name: "sleeper", remove: func(d *RuntimeDependencies) { d.Sleeper = nil }},
		{name: "random", remove: func(d *RuntimeDependencies) { d.Random = nil }},
		{name: "logger", remove: func(d *RuntimeDependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := completeRuntimeDependencies(t)
			tt.remove(&deps)
			if _, err := NewRuntime(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewRuntime = %v, want ErrMissingDependency", err)
			}
		})
	}
}

type scriptedScanner struct {
	failures int32
	calls    atomic.Int32
}

func (s *scriptedScanner) EnsureAllAuthoritative(context.Context) error {
	if s.calls.Add(1) <= s.failures {
		return errors.New("telegram unreachable")
	}
	return nil
}

type recordedReadiness struct {
	ready    atomic.Bool
	starting atomic.Int32
}

func (r *recordedReadiness) MarkReady()    { r.ready.Store(true) }
func (r *recordedReadiness) MarkStarting() { r.starting.Add(1) }

func TestAuthoritativeSyncRetriesUntilReady(t *testing.T) {
	tests := []struct {
		name      string
		failures  int32
		wantCalls int32
	}{
		{name: "first attempt succeeds", failures: 0, wantCalls: 1},
		{name: "two failures then success", failures: 2, wantCalls: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner := &scriptedScanner{failures: tt.failures}
			readiness := &recordedReadiness{}
			sync := &authoritativeSync{
				scanner: scanner, readiness: readiness, logger: newDiscardLogger(),
				sleeper: flow.SleeperFunc(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }),
			}
			if err := sync.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitFor(t, readiness.ready.Load)
			if err := sync.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := scanner.calls.Load(); got != tt.wantCalls {
				t.Fatalf("scanner calls = %d, want %d", got, tt.wantCalls)
			}
			if readiness.starting.Load() != 1 {
				t.Fatalf("MarkStarting calls = %d, want 1", readiness.starting.Load())
			}
		})
	}
}

func TestAuthoritativeSyncCloseStopsRetrying(t *testing.T) {
	scanner := &scriptedScanner{failures: 1 << 30}
	readiness := &recordedReadiness{}
	sync := &authoritativeSync{
		scanner: scanner, readiness: readiness, logger: newDiscardLogger(),
		sleeper: flow.SleeperFunc(func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	if err := sync.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { return scanner.calls.Load() >= 1 })
	if err := sync.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if readiness.ready.Load() {
		t.Fatal("became ready without a successful sync")
	}
}

type countingSweeper struct {
	calls atomic.Int32
	idle  atomic.Int64
}

func (c *countingSweeper) SweepIdle(_ context.Context, idle time.Duration) (int, error) {
	c.idle.Store(int64(idle))
	c.calls.Add(1)
	return 1, nil
}

func TestMultipartExpirySweepsWithConfiguredIdleTime(t *testing.T) {
	stage := &countingSweeper{}
	expiry := &multipartExpiry{
		stage: stage, idle: 168 * time.Hour, logger: newDiscardLogger(),
		sleeper: flow.SleeperFunc(func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	if err := expiry.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { return stage.calls.Load() == 1 })
	if err := expiry.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if time.Duration(stage.idle.Load()) != 168*time.Hour {
		t.Fatalf("idle = %v, want 168h", time.Duration(stage.idle.Load()))
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}
