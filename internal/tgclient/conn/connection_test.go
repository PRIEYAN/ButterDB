package conn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"api id", func(d *Dependencies) { d.APIID = 0 }},
		{"api hash", func(d *Dependencies) { d.APIHash = "" }},
		{"sessions", func(d *Dependencies) { d.Sessions = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
		{"activity", func(d *Dependencies) { d.Activity = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := testDependencies()
			tt.mutate(&deps)
			c, err := New(deps)
			if !errors.Is(err, errs.ErrMissingDependency) || c != nil {
				t.Fatalf("New() = %v, %v; want ErrMissingDependency", c, err)
			}
		})
	}
}

func TestNewStartsNothing(t *testing.T) {
	c, err := New(testDependencies())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInvokeStartsScopeLazilyAndOnce(t *testing.T) {
	client := newFakeClient(nil)
	c, _ := newTestConnection(t, client)
	if client.runCount() != 0 {
		t.Fatalf("scope ran before the first call")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 3; i++ {
		err := c.Invoke(context.Background(), func(_ context.Context, api *tg.Client) error {
			if api != client.api {
				t.Errorf("Invoke passed a different api client")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
	}
	if got := client.runCount(); got != 1 {
		t.Fatalf("scope ran %d times, want 1", got)
	}
}

func TestInvokeFailedConnectIsNotFatal(t *testing.T) {
	failing := newFakeClient(nil)
	failing.runErr = errors.New("dial refused")
	healthy := newFakeClient(nil)
	current := failing
	c := newWithFactory(testDependencies(), func() (gotdClient, error) { return current, nil })
	defer c.Close(context.Background())

	noop := func(context.Context, *tg.Client) error { return nil }
	if err := c.Invoke(context.Background(), noop); !errors.Is(err, failing.runErr) {
		t.Fatalf("Invoke err = %v, want dial refused", err)
	}
	current = healthy
	if err := c.Invoke(context.Background(), noop); err != nil {
		t.Fatalf("Invoke after recovery: %v", err)
	}
}

func TestInvokeNormalizesFloodWait(t *testing.T) {
	c, _ := newTestConnection(t, newFakeClient(nil))
	err := c.Invoke(context.Background(), func(context.Context, *tg.Client) error {
		return tgerr.New(420, "FLOOD_WAIT_30")
	})
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || flood.Duration != 30*time.Second || !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("Invoke err = %v, want 30s flood wait", err)
	}
}

func TestInvokeAfterCloseFails(t *testing.T) {
	c, _ := newTestConnection(t, newFakeClient(nil))
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := c.Invoke(context.Background(), func(context.Context, *tg.Client) error { return nil })
	if !errors.Is(err, ErrScopeClosed) {
		t.Fatalf("Invoke after Close = %v, want ErrScopeClosed", err)
	}
	if err := c.Start(context.Background()); !errors.Is(err, ErrScopeClosed) {
		t.Fatalf("Start after Close = %v, want ErrScopeClosed", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestTwoAccountsIndependent(t *testing.T) {
	primaryClient := newFakeClient(nil)
	replicaClient := newFakeClient(nil)
	primary, _ := newTestConnection(t, primaryClient)
	replica, _ := newTestConnection(t, replicaClient)

	noop := func(context.Context, *tg.Client) error { return nil }
	if err := primary.InvokeMedia(context.Background(), 4, noop); err != nil {
		t.Fatalf("primary InvokeMedia: %v", err)
	}
	if err := replica.InvokeMedia(context.Background(), 4, noop); err != nil {
		t.Fatalf("replica InvokeMedia: %v", err)
	}
	if len(primaryClient.poolCalls()) != 1 || len(replicaClient.poolCalls()) != 1 {
		t.Fatalf("each account must dial its own pool: %v %v", primaryClient.poolCalls(), replicaClient.poolCalls())
	}

	if err := primary.Close(context.Background()); err != nil {
		t.Fatalf("primary Close: %v", err)
	}
	if got := primaryClient.poolList()[0].closeCount(); got != 1 {
		t.Fatalf("primary pool closed %d times, want 1", got)
	}
	if got := replicaClient.poolList()[0].closeCount(); got != 0 {
		t.Fatalf("closing one account closed the other's pool")
	}
	if err := replica.Invoke(context.Background(), noop); err != nil {
		t.Fatalf("replica Invoke after primary Close: %v", err)
	}
}

func TestTwoAccountsFailuresDoNotLeak(t *testing.T) {
	failing := newFakeClient(nil)
	failing.runErr = errors.New("dial refused")
	healthy := newFakeClient(nil)
	bad, _ := newTestConnection(t, failing)
	good, _ := newTestConnection(t, healthy)

	noop := func(context.Context, *tg.Client) error { return nil }
	if err := bad.Invoke(context.Background(), noop); err == nil {
		t.Fatal("failing account should error")
	}
	if err := good.Invoke(context.Background(), noop); err != nil {
		t.Fatalf("healthy account affected by the other: %v", err)
	}
}

func TestInvokeBlockedInConnectReturnsScopeClosedOnClose(t *testing.T) {
	release := make(chan struct{})
	c := newWithFactory(testDependencies(), func() (gotdClient, error) {
		<-release
		return newFakeClient(nil), nil
	})
	result := make(chan error, 1)
	go func() {
		result <- c.Invoke(context.Background(), func(context.Context, *tg.Client) error { return nil })
	}()
	waitFor(t, func() bool { c.live.mu.Lock(); defer c.live.mu.Unlock(); return c.live.scope != nil })
	closed := make(chan error, 1)
	go func() { closed <- c.Close(context.Background()) }()
	waitFor(t, c.live.isClosed)
	close(release)
	if err := <-result; !errors.Is(err, ErrScopeClosed) {
		t.Fatalf("Invoke err = %v, want ErrScopeClosed", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInvokeMediaAfterCloseReturnsScopeClosed(t *testing.T) {
	c, _ := newTestConnection(t, newFakeClient(nil))
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := c.InvokeMedia(context.Background(), 4, func(context.Context, *tg.Client) error { return nil })
	if !errors.Is(err, ErrScopeClosed) {
		t.Fatalf("InvokeMedia after Close = %v, want ErrScopeClosed", err)
	}
}
