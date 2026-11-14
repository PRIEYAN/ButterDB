package conn

import (
	"context"
	"sync"
	"time"
)

type flightGroup[T any] struct {
	base    context.Context
	timeout time.Duration

	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
	calls  map[string]*flightCall[T]
}

type flightCall[T any] struct {
	done    chan struct{}
	value   T
	err     error
	waiters int
}

func newFlightGroup[T any](base context.Context, timeout time.Duration) *flightGroup[T] {
	return &flightGroup[T]{base: base, timeout: timeout, calls: make(map[string]*flightCall[T])}
}

func (g *flightGroup[T]) do(ctx context.Context, key string, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return zero, ErrScopeClosed
	}
	call, joined := g.calls[key]
	if !joined {
		call = &flightCall[T]{done: make(chan struct{})}
		g.calls[key] = call
		g.wg.Add(1)
		go g.run(key, call, fn)
	}
	call.waiters++
	g.mu.Unlock()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (g *flightGroup[T]) run(key string, call *flightCall[T], fn func(ctx context.Context) (T, error)) {
	defer g.wg.Done()
	ctx, cancel := context.WithTimeout(g.base, g.timeout)
	defer cancel()
	call.value, call.err = fn(ctx)
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)
}

func (g *flightGroup[T]) waiting(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if call, ok := g.calls[key]; ok {
		return call.waiters
	}
	return 0
}

func (g *flightGroup[T]) close(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
