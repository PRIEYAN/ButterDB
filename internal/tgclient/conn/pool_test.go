package conn

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestIsFileMigrateRecognizesOnlyTheRedirect(t *testing.T) {
	t.Parallel()

	if !isFileMigrate(tgerr.New(303, "FILE_MIGRATE_4")) {
		t.Fatal("FILE_MIGRATE_4 was not recognized as a redirect")
	}
	for _, err := range []error{
		tgerr.New(420, "FLOOD_WAIT_30"),
		tgerr.New(400, "FILE_REFERENCE_EXPIRED"),
		errors.New("engine forcibly closed"),
		nil,
	} {
		if isFileMigrate(err) {
			t.Fatalf("%v was mistaken for a redirect", err)
		}
	}
}

func TestPoolConstantsKeepSpecifiedValues(t *testing.T) {
	if MediaPoolSize != 4 || UploadThreads != 8 || poolRetryAfter != 30*time.Second {
		t.Fatalf("constants = %d, %d, %s", MediaPoolSize, UploadThreads, poolRetryAfter)
	}
}

func TestInvokeMediaPicksPoolKind(t *testing.T) {
	tests := []struct {
		name string
		dc   int
		want []poolCall
	}{
		{"primary dc shares the session", 2, []poolCall{{"pool", 2, MediaPoolSize}}},
		{"other dc gets its own pool", 4, []poolCall{{"dc", 4, MediaPoolSize}}},
		{"cdn dc gets a media only pool", 203, []poolCall{{"media", 203, cdnPoolSize}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(nil)
			client.config.DCOptions = []tg.DCOption{{ID: 203, CDN: true}, {ID: 4}}
			c, _ := newTestConnection(t, client)
			err := c.InvokeMedia(context.Background(), tt.dc, func(context.Context, *tg.Client) error { return nil })
			if err != nil {
				t.Fatalf("InvokeMedia: %v", err)
			}
			got := client.poolCalls()
			if len(got) != 1 || got[0] != tt.want[0] {
				t.Fatalf("pool calls = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInvokeMediaWithoutDCUsesPrimary(t *testing.T) {
	client := newFakeClient(nil)
	c, _ := newTestConnection(t, client)
	err := c.InvokeMedia(context.Background(), 0, func(_ context.Context, api *tg.Client) error {
		if api != client.api {
			t.Error("dc 0 must use the primary connection")
		}
		return nil
	})
	if err != nil || len(client.poolCalls()) != 0 {
		t.Fatalf("err = %v, pool calls = %v", err, client.poolCalls())
	}
}

func TestConcurrentFirstReadsShareOneDial(t *testing.T) {
	client := newFakeClient(nil)
	client.dialGate = make(chan struct{})
	c, _ := newTestConnection(t, client)

	const readers = 8
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Go(func() {
			err := c.InvokeMedia(context.Background(), 4, func(_ context.Context, api *tg.Client) error {
				if api == client.api {
					t.Error("reader fell back to the primary connection")
				}
				return nil
			})
			if err != nil {
				t.Errorf("InvokeMedia: %v", err)
			}
		})
	}
	waitFor(t, func() bool { return c.dials.waiting("4") == readers })
	close(client.dialGate)
	wg.Wait()

	if got := len(client.poolCalls()); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
}

func TestFailedDialBacksOffThirtySecondsPerDC(t *testing.T) {
	client := newFakeClient(nil)
	client.poolErr = errors.New("dial refused")
	c, fakeClock := newTestConnection(t, client)

	usePrimary := func(_ context.Context, api *tg.Client) error {
		if api != client.api {
			t.Error("failed dial must fall back to the primary connection")
		}
		return nil
	}
	if err := c.InvokeMedia(context.Background(), 4, usePrimary); err != nil {
		t.Fatalf("first read: %v", err)
	}
	fakeClock.Advance(poolRetryAfter - time.Second)
	if err := c.InvokeMedia(context.Background(), 4, usePrimary); err != nil {
		t.Fatalf("read during backoff: %v", err)
	}
	if got := len(client.poolCalls()); got != 1 {
		t.Fatalf("dials during backoff = %d, want 1", got)
	}

	if err := c.InvokeMedia(context.Background(), 5, usePrimary); err != nil {
		t.Fatalf("read on another dc: %v", err)
	}
	if got := len(client.poolCalls()); got != 2 {
		t.Fatalf("another dc must not share the backoff, dials = %d, want 2", got)
	}

	fakeClock.Advance(2 * time.Second)
	if err := c.InvokeMedia(context.Background(), 4, usePrimary); err != nil {
		t.Fatalf("read after backoff: %v", err)
	}
	if got := len(client.poolCalls()); got != 3 {
		t.Fatalf("dials after backoff = %d, want 3", got)
	}
}

func TestCanceledDialEarnsNoBackoff(t *testing.T) {
	client := newFakeClient(nil)
	client.dialGate = make(chan struct{})
	c, _ := newTestConnection(t, client)

	if err := c.Invoke(context.Background(), func(context.Context, *tg.Client) error { return nil }); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = c.InvokeMedia(ctx, 4, func(context.Context, *tg.Client) error { return nil })

	close(client.dialGate)
	err := c.InvokeMedia(context.Background(), 4, func(_ context.Context, api *tg.Client) error {
		if api == client.api {
			t.Error("a canceled caller must not push the dc into backoff")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeMedia: %v", err)
	}
}

func TestInvokeMediaRetriesFileMigrateOnPrimary(t *testing.T) {
	client := newFakeClient(nil)
	c, _ := newTestConnection(t, client)

	var seen []bool
	err := c.InvokeMedia(context.Background(), 4, func(_ context.Context, api *tg.Client) error {
		onPrimary := api == client.api
		seen = append(seen, onPrimary)
		if !onPrimary {
			return tgerr.New(303, "FILE_MIGRATE_4")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InvokeMedia: %v", err)
	}
	if len(seen) != 2 || seen[0] || !seen[1] {
		t.Fatalf("attempts on primary = %v, want [false true]", seen)
	}
}

func TestInvokeMediaDoesNotRetryOtherErrors(t *testing.T) {
	client := newFakeClient(nil)
	c, _ := newTestConnection(t, client)

	calls := 0
	wantErr := tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	err := c.InvokeMedia(context.Background(), 4, func(context.Context, *tg.Client) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("err = %v after %d calls", err, calls)
	}
}

func TestPoolsAreReusedAndClosedWithTheScope(t *testing.T) {
	client := newFakeClient(nil)
	c, _ := newTestConnection(t, client)
	noop := func(context.Context, *tg.Client) error { return nil }

	for i := 0; i < 3; i++ {
		if err := c.InvokeMedia(context.Background(), 4, noop); err != nil {
			t.Fatalf("InvokeMedia: %v", err)
		}
	}
	if got := len(client.poolCalls()); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := client.poolList()[0].closeCount(); got != 1 {
		t.Fatalf("pool closed %d times, want 1", got)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
