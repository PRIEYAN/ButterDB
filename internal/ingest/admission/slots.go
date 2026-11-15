package admission

import (
	"container/list"
	"sync"
)

type slotWaiter struct {
	key     string
	ready   chan struct{}
	granted bool
	element *list.Element
}

type slotTable struct {
	mu      sync.Mutex
	size    int
	perKey  int
	held    int
	keyHeld map[string]int
	waiters list.List
}

func newSlotTable(size, perKey int) *slotTable {
	return &slotTable{size: size, perKey: perKey, keyHeld: make(map[string]int)}
}

func (t *slotTable) canGrant(key string) bool {
	return t.held < t.size && t.keyHeld[key] < t.perKey
}

func (t *slotTable) grant(key string) {
	t.held++
	t.keyHeld[key]++
}

func (t *slotTable) tryAcquire(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.canGrant(key) {
		return false
	}
	t.grant(key)
	return true
}

func (t *slotTable) enqueue(key string) *slotWaiter {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := &slotWaiter{key: key, ready: make(chan struct{})}
	w.element = t.waiters.PushBack(w)
	return w
}

func (t *slotTable) release(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.returnSlot(key)
	t.dispatch()
}

func (t *slotTable) returnSlot(key string) {
	t.held--
	t.keyHeld[key]--
	if t.keyHeld[key] <= 0 {
		delete(t.keyHeld, key)
	}
}

func (t *slotTable) dispatch() {
	for e := t.waiters.Front(); e != nil && t.held < t.size; {
		next := e.Next()
		w := e.Value.(*slotWaiter)
		if t.keyHeld[w.key] < t.perKey {
			t.grant(w.key)
			w.granted = true
			t.waiters.Remove(e)
			close(w.ready)
		}
		e = next
	}
}

func (t *slotTable) abandon(w *slotWaiter) (granted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w.granted {
		return true
	}
	t.waiters.Remove(w.element)
	return false
}
