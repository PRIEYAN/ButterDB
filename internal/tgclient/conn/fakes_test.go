package conn

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

type invokerFunc func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f invokerFunc) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

func respondWith(output bin.Decoder, value bin.Encoder) error {
	var buf bin.Buffer
	if err := value.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

type fakePool struct {
	mu     sync.Mutex
	closed int
}

func (p *fakePool) Invoke(context.Context, bin.Encoder, bin.Decoder) error { return nil }

func (p *fakePool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed++
	return nil
}

func (p *fakePool) closeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

type poolCall struct {
	kind string
	dc   int
	max  int64
}

type fakeClient struct {
	api    *tg.Client
	config tg.Config

	runErr   error
	poolErr  error
	dialGate chan struct{}

	mu    sync.Mutex
	calls []poolCall
	pools []*fakePool
	runs  int
}

func newFakeClient(handler invokerFunc) *fakeClient {
	if handler == nil {
		handler = func(context.Context, bin.Encoder, bin.Decoder) error { return nil }
	}
	return &fakeClient{api: tg.NewClient(handler), config: tg.Config{ThisDC: 2}}
}

func (f *fakeClient) Run(ctx context.Context, fn func(ctx context.Context) error) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if f.runErr != nil {
		return f.runErr
	}
	return fn(ctx)
}

func (f *fakeClient) API() *tg.Client { return f.api }

func (f *fakeClient) Config() tg.Config { return f.config }

func (f *fakeClient) record(kind string, dc int, max int64) (telegram.CloseInvoker, error) {
	f.mu.Lock()
	f.calls = append(f.calls, poolCall{kind: kind, dc: dc, max: max})
	f.mu.Unlock()
	if f.poolErr != nil {
		return nil, f.poolErr
	}
	pool := &fakePool{}
	f.mu.Lock()
	f.pools = append(f.pools, pool)
	f.mu.Unlock()
	return pool, nil
}

func (f *fakeClient) waitGate(ctx context.Context) error {
	if f.dialGate == nil {
		return nil
	}
	select {
	case <-f.dialGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeClient) Pool(max int64) (telegram.CloseInvoker, error) {
	return f.record("pool", f.config.ThisDC, max)
}

func (f *fakeClient) DC(ctx context.Context, dc int, max int64) (telegram.CloseInvoker, error) {
	if err := f.waitGate(ctx); err != nil {
		return nil, err
	}
	return f.record("dc", dc, max)
}

func (f *fakeClient) MediaOnly(ctx context.Context, dc int, max int64) (telegram.CloseInvoker, error) {
	return f.record("media", dc, max)
}

func (f *fakeClient) poolCalls() []poolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]poolCall(nil), f.calls...)
}

func (f *fakeClient) poolList() []*fakePool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakePool(nil), f.pools...)
}

func (f *fakeClient) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func testDependencies() Dependencies {
	return Dependencies{
		APIID:    1,
		APIHash:  "hash",
		Sessions: &memoryStore{},
		Clock:    clock.NewFake(time.Unix(1_700_000_000, 0)),
		Logger:   discardLogger(),
		Activity: NewActivity(1),
	}
}

func newTestConnection(t *testing.T, client gotdClient) (*Connection, *clock.Fake) {
	t.Helper()
	deps := testDependencies()
	fake := deps.Clock.(*clock.Fake)
	c := newWithFactory(deps, func() (gotdClient, error) { return client, nil })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c, fake
}
