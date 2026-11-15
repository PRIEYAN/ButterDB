package objectreader

import (
	"container/list"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

type LRUConfig[V any] struct {
	Capacity  int
	MaxWeight int
	TTL       time.Duration
	Clock     clock.Clock
	Weight    func(V) int
	Clone     func(V) V
}

type LRU[K comparable, V any] struct {
	mu          sync.Mutex
	entries     map[K]*list.Element
	recent      list.List
	capacity    int
	maxWeight   int
	totalWeight int
	ttl         time.Duration
	clock       clock.Clock
	weight      func(V) int
	clone       func(V) V
}

type lruEntry[K comparable, V any] struct {
	key       K
	value     V
	weight    int
	expiresAt time.Time
}

func NewLRU[K comparable, V any](config LRUConfig[V]) *LRU[K, V] {
	if config.Capacity < 0 {
		config.Capacity = 0
	}
	if config.MaxWeight < 0 {
		config.MaxWeight = 0
	}
	if config.Clock == nil {
		config.TTL = 0
	}
	if config.Weight == nil {
		config.Weight = func(V) int { return 1 }
	}
	if config.Clone == nil {
		config.Clone = func(value V) V { return value }
	}
	return &LRU[K, V]{
		entries:   make(map[K]*list.Element, config.Capacity),
		capacity:  config.Capacity,
		maxWeight: config.MaxWeight,
		ttl:       config.TTL,
		clock:     config.Clock,
		weight:    config.Weight,
		clone:     config.Clone,
	}
}

func (cache *LRU[K, V]) Get(key K) (V, bool) {
	var zero V
	if cache == nil {
		return zero, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()

	element := cache.entries[key]
	if element == nil {
		return zero, false
	}
	entry := element.Value.(lruEntry[K, V])
	if !entry.expiresAt.IsZero() && !cache.clock.Now().Before(entry.expiresAt) {
		cache.removeLocked(element)
		return zero, false
	}
	cache.recent.MoveToFront(element)
	return cache.clone(entry.value), true
}

func (cache *LRU[K, V]) Put(key K, value V) bool {
	if cache == nil || cache.capacity <= 0 {
		return false
	}
	stored := cache.clone(value)
	weight := max(cache.weight(stored), 0)

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element := cache.entries[key]; element != nil {
		cache.removeLocked(element)
	}
	if cache.maxWeight > 0 && weight > cache.maxWeight {
		return false
	}

	var expiresAt time.Time
	if cache.ttl > 0 {
		expiresAt = cache.clock.Now().Add(cache.ttl)
	}
	element := cache.recent.PushFront(lruEntry[K, V]{
		key:       key,
		value:     stored,
		weight:    weight,
		expiresAt: expiresAt,
	})
	cache.entries[key] = element
	cache.totalWeight += weight
	for cache.recent.Len() > cache.capacity ||
		(cache.maxWeight > 0 && cache.totalWeight > cache.maxWeight) {
		cache.removeLocked(cache.recent.Back())
	}
	return cache.entries[key] == element
}

func (cache *LRU[K, V]) Delete(key K) (V, bool) {
	var zero V
	if cache == nil {
		return zero, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element := cache.entries[key]
	if element == nil {
		return zero, false
	}
	entry := element.Value.(lruEntry[K, V])
	cache.removeLocked(element)
	return cache.clone(entry.value), true
}

func (cache *LRU[K, V]) Clear() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.entries = make(map[K]*list.Element, cache.capacity)
	cache.recent.Init()
	cache.totalWeight = 0
	cache.mu.Unlock()
}

func (cache *LRU[K, V]) Len() int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.recent.Len()
}

func (cache *LRU[K, V]) Weight() int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.totalWeight
}

func (cache *LRU[K, V]) removeLocked(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(lruEntry[K, V])
	delete(cache.entries, entry.key)
	cache.totalWeight -= entry.weight
	cache.recent.Remove(element)
}
