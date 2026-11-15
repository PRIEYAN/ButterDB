package blockcache

import (
	"context"
	"fmt"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var errClosed = fmt.Errorf("blockcache: closed: %w", errs.ErrServiceUnavailable)

type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	data    []byte
	err     error
}

type flights struct {
	mu     sync.Mutex
	m      map[blockKey]*flight
	base   context.Context
	stop   context.CancelFunc
	loads  sync.WaitGroup
	closed bool
}

func newFlights() *flights {
	base, stop := context.WithCancel(context.Background())
	return &flights{m: make(map[blockKey]*flight), base: base, stop: stop}
}

func (g *flights) do(ctx context.Context, key blockKey, load func(context.Context) ([]byte, error)) ([]byte, error) {
	f, err := g.join(key, load)
	if err != nil {
		return nil, err
	}
	select {
	case <-f.done:
		return f.data, f.err
	case <-ctx.Done():
		g.leave(key, f)
		return nil, ctx.Err()
	}
}

func (g *flights) join(key blockKey, load func(context.Context) ([]byte, error)) (*flight, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, errClosed
	}
	if f, running := g.m[key]; running {
		f.waiters++
		return f, nil
	}
	loadCtx, cancel := context.WithCancel(g.base)
	f := &flight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	g.m[key] = f
	g.loads.Go(func() { g.run(loadCtx, key, f, load) })
	return f, nil
}

func (g *flights) run(ctx context.Context, key blockKey, f *flight, load func(context.Context) ([]byte, error)) {
	data, err := load(ctx)
	g.mu.Lock()
	f.data, f.err = data, err
	if g.m[key] == f {
		delete(g.m, key)
	}
	close(f.done)
	g.mu.Unlock()
	f.cancel()
}

func (g *flights) leave(key blockKey, f *flight) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f.waiters--
	if f.waiters > 0 {
		return
	}
	if g.m[key] == f {
		delete(g.m, key)
	}
	f.cancel()
}

func (g *flights) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.stop()
	g.loads.Wait()
}

func (g *flights) waiters(key blockKey) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.m[key]; f != nil {
		return f.waiters
	}
	return 0
}
