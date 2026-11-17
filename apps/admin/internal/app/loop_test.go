package app

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func TestStartupWaitsForEventLoop(t *testing.T) {
	h := newHarness(t, withLinked())
	var startLoop func()
	h.afterLoop = func(start func()) { startLoop = start }

	h.start()

	if startLoop == nil {
		t.Fatalf("compose did not hand the start step to the loop hook")
	}
	h.settle()
	if got := h.engine.trips.Load(); got != 0 {
		t.Fatalf("%d engine requests ran before the event loop started", got)
	}
	if task := h.queue.pop(); task != nil {
		t.Fatalf("a UI update was dispatched before the event loop started")
	}
	if got := h.route(); got != "" {
		t.Fatalf("route %q before the loop runs, want none", got)
	}

	startLoop()

	h.waitRoute(model.RouteProjects)
}
