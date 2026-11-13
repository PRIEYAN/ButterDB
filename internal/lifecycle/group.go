package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

type Component interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
}

type entry struct {
	name      string
	component Component
}

type Group struct {
	mu           sync.Mutex
	closeTimeout time.Duration
	entries      []entry
	started      []entry
	addErr       error
	startCalled  bool
}

func NewGroup(closeTimeout time.Duration) *Group {
	if closeTimeout <= 0 {
		closeTimeout = limits.DefaultCloseTimeout
	}
	return &Group{closeTimeout: closeTimeout}
}

func (g *Group) Add(name string, c Component) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case c == nil:
		g.addErr = errors.Join(g.addErr, fmt.Errorf("lifecycle: add %q: %w: component", name, errs.ErrMissingDependency))
	case g.startCalled:
		g.addErr = errors.Join(g.addErr, fmt.Errorf("lifecycle: add %q: group already started", name))
	default:
		g.entries = append(g.entries, entry{name: name, component: c})
	}
}

func (g *Group) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.startCalled {
		return errors.New("lifecycle: group already started")
	}
	g.startCalled = true
	if g.addErr != nil {
		return g.addErr
	}
	for _, e := range g.entries {
		if err := ctx.Err(); err != nil {
			return g.rollback(ctx, fmt.Errorf("lifecycle: start %s: %w", e.name, err))
		}
		if err := e.component.Start(ctx); err != nil {
			return g.rollback(ctx, fmt.Errorf("lifecycle: start %s: %w", e.name, err))
		}
		g.started = append(g.started, e)
	}
	return nil
}

func (g *Group) Close(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closeStarted(ctx)
}

func (g *Group) rollback(ctx context.Context, cause error) error {
	// Start may have failed because ctx was canceled; started components still deserve a full close window.
	return errors.Join(cause, g.closeStarted(context.WithoutCancel(ctx)))
}

func (g *Group) closeStarted(ctx context.Context) error {
	var failures []error
	for i := len(g.started) - 1; i >= 0; i-- {
		e := g.started[i]
		if err := g.closeOne(ctx, e); err != nil {
			failures = append(failures, err)
		}
	}
	g.started = nil
	return errors.Join(failures...)
}

func (g *Group) closeOne(ctx context.Context, e entry) error {
	bounded, cancel := context.WithTimeout(ctx, g.closeTimeout)
	defer cancel()
	if err := e.component.Close(bounded); err != nil {
		return fmt.Errorf("lifecycle: close %s: %w", e.name, err)
	}
	return nil
}
