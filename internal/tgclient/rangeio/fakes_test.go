package rangeio

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/rpc"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var testPeer = tgclient.InputPeer{ChannelID: 100, AccessHash: 200}

type handlerInvoker struct {
	handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error
}

func (h handlerInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return h.handle(ctx, input, output)
}

type routeCall struct {
	media bool
	dc    int
}

type routingInvoker struct {
	api   *tg.Client
	mu    sync.Mutex
	calls []routeCall
}

func newRoutingInvoker(handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error) *routingInvoker {
	return &routingInvoker{api: tg.NewClient(handlerInvoker{handle: handle})}
}

func (r *routingInvoker) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	r.record(routeCall{})
	return fn(ctx, r.api)
}

func (r *routingInvoker) InvokeMedia(ctx context.Context, dcID int, fn func(ctx context.Context, api *tg.Client) error) error {
	r.record(routeCall{media: true, dc: dcID})
	return fn(ctx, r.api)
}

func (r *routingInvoker) record(call routeCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *routingInvoker) routes() []routeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]routeCall(nil), r.calls...)
}

type countingGate struct {
	mu       sync.Mutex
	acquired int
	held     int
}

func (g *countingGate) AcquireForeground(ctx context.Context, n int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.acquired += n
	g.held += n
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.held -= n
		g.mu.Unlock()
	}, nil
}

func (g *countingGate) TryAcquireBackground(int) (func(), bool) {
	return nil, false
}

type scriptedRefresher struct {
	mu    sync.Mutex
	calls int
	fresh []byte
	err   error
}

func (r *scriptedRefresher) RefreshDocument(_ context.Context, _, _, _ int64) (int64, int64, []byte, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return 0, 0, nil, 0, r.err
	}
	return 11, 12, r.fresh, 2, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type readerHarness struct {
	invoker   *routingInvoker
	gate      *countingGate
	refresher *scriptedRefresher
	reader    *Reader
}

func newReaderHarness(t *testing.T, handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error) *readerHarness {
	t.Helper()
	h := &readerHarness{
		invoker:   newRoutingInvoker(handle),
		gate:      &countingGate{},
		refresher: &scriptedRefresher{fresh: []byte("v2")},
	}
	reader, err := New(Dependencies{Invoker: h.invoker, Gate: h.gate, Refresher: h.refresher, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.reader = reader
	return h
}

type scriptedBlockRetry struct {
	maxRetries int
	slept      int
}

func (r *scriptedBlockRetry) Do(ctx context.Context, action func() error) error {
	for attempt := 0; ; attempt++ {
		err := action()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !isTransientForTest(err) || attempt >= r.maxRetries {
			return err
		}
		r.slept++
	}
}

func isTransientForTest(err error) bool {
	return errors.Is(err, rpc.ErrEngineClosed) || errors.Is(err, context.Canceled) ||
		strings.Contains(err.Error(), "engine forcibly closed")
}
