package model

import (
	"slices"
	"sync"
)

type Observable[T any] struct {
	mu   sync.Mutex
	v    T
	subs map[int]func(T)
	next int
}

func NewObservable[T any](initial T) *Observable[T] {
	return &Observable[T]{v: initial, subs: make(map[int]func(T))}
}

func (o *Observable[T]) Get() T {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.v
}

func (o *Observable[T]) Set(v T) {
	o.mu.Lock()
	o.v = v
	subscribers := o.snapshotSubscribers()
	o.mu.Unlock()
	for _, fn := range subscribers {
		fn(v)
	}
}

func (o *Observable[T]) Subscribe(fn func(T)) (cancel func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.subs == nil {
		o.subs = make(map[int]func(T))
	}
	id := o.next
	o.next++
	o.subs[id] = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			delete(o.subs, id)
		})
	}
}

func (o *Observable[T]) snapshotSubscribers() []func(T) {
	ids := make([]int, 0, len(o.subs))
	for id := range o.subs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]func(T), 0, len(ids))
	for _, id := range ids {
		out = append(out, o.subs[id])
	}
	return out
}
