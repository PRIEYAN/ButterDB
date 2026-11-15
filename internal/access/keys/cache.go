package keys

import (
	"container/list"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/storage/keyrepo"
)

const cacheCapacity = 1024

type recordCache struct {
	mu       sync.Mutex
	capacity int
	epoch    uint64
	order    *list.List
	entries  map[access.AccessKeyID]*list.Element
}

func newRecordCache(capacity int) *recordCache {
	return &recordCache{
		capacity: capacity,
		order:    list.New(),
		entries:  make(map[access.AccessKeyID]*list.Element, capacity),
	}
}

func (c *recordCache) get(id access.AccessKeyID) (keyrepo.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[id]
	if !ok {
		return keyrepo.Record{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(keyrepo.Record), true
}

func (c *recordCache) currentEpoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

func (c *recordCache) putIfEpoch(rec keyrepo.Record, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return
	}
	if element, ok := c.entries[rec.ID]; ok {
		element.Value = rec
		c.order.MoveToFront(element)
		return
	}
	c.entries[rec.ID] = c.order.PushFront(rec)
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(keyrepo.Record).ID)
	}
}

func (c *recordCache) invalidate(id access.AccessKeyID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	if element, ok := c.entries[id]; ok {
		c.order.Remove(element)
		delete(c.entries, id)
	}
}

func (c *recordCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
