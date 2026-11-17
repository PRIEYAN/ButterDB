package app

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
)

type loopGate struct {
	mu     sync.Mutex
	open   bool
	queued []func()
}

func (g *loopGate) whenOpen(f func()) {
	g.mu.Lock()
	if !g.open {
		g.queued = append(g.queued, f)
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	f()
}

func (g *loopGate) release() {
	g.mu.Lock()
	g.open = true
	queued := g.queued
	g.queued = nil
	g.mu.Unlock()
	for _, f := range queued {
		f()
	}
}

func lifecycleStart(a fyne.App) func(func()) {
	return func(f func()) { a.Lifecycle().SetOnStarted(f) }
}

func skipIfDone(ctx context.Context, f func()) func() {
	return func() {
		if ctx.Err() == nil {
			f()
		}
	}
}
