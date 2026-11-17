package app

import (
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type countingStore struct {
	model.ProfileStore
	lists    atomic.Int32
	lastUsed atomic.Int32
}

func (s *countingStore) List() ([]model.Profile, error) {
	s.lists.Add(1)
	return s.ProfileStore.List()
}

func (s *countingStore) LastUsed() (string, error) {
	s.lastUsed.Add(1)
	return s.ProfileStore.LastUsed()
}

func TestAutoConnectWaitsForEventLoop(t *testing.T) {
	store := &countingStore{}
	h := newHarness(t, func(o *Options) {
		store.ProfileStore = o.Store
		o.Store = store
	})
	var startLoop func()
	h.afterLoop = func(start func()) { startLoop = start }

	h.start()

	if startLoop == nil {
		t.Fatalf("compose did not hand the start step to the loop hook")
	}
	if got := h.route(); got != model.RouteConnect {
		t.Fatalf("route before the loop runs %q, want connect", got)
	}
	h.settle()
	if got := store.lastUsed.Load(); got != 0 {
		t.Fatalf("auto connect ran %d times before the event loop started", got)
	}
	if got := store.lists.Load(); got != 0 {
		t.Fatalf("connect screen listed profiles %d times before the event loop started", got)
	}
	if task := h.queue.pop(); task != nil {
		t.Fatalf("a UI update was dispatched before the event loop started")
	}

	startLoop()

	h.waitFor("screen refresh and auto connect after the loop starts", func() bool {
		return store.lists.Load() >= 2 && store.lastUsed.Load() >= 3
	})
	if got := store.lists.Load(); got != 2 {
		t.Fatalf("profiles listed %d times after the loop started, want 2 (screen refresh and auto connect)", got)
	}
	if got := store.lastUsed.Load(); got != 3 {
		t.Fatalf("last used read %d times after the loop started, want 3", got)
	}
}
