package flow

import (
	"container/list"
	"context"
	"sync"
)

type weightedSemaphore struct {
	mu      sync.Mutex
	size    int
	held    int
	waiters list.List
}

type semaphoreWaiter struct {
	weight int
	ready  chan struct{}
}

func newWeightedSemaphore(size int) *weightedSemaphore {
	return &weightedSemaphore{size: size}
}

func (s *weightedSemaphore) acquire(ctx context.Context, weight int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.waiters.Len() == 0 && s.size-s.held >= weight {
		s.held += weight
		s.mu.Unlock()
		return nil
	}
	w := &semaphoreWaiter{weight: weight, ready: make(chan struct{})}
	element := s.waiters.PushBack(w)
	s.mu.Unlock()

	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-w.ready:
			s.mu.Unlock()
			return nil
		default:
		}
		front := s.waiters.Front() == element
		s.waiters.Remove(element)
		if front {
			s.grantWaiters()
		}
		s.mu.Unlock()
		return ctx.Err()
	}
}

func (s *weightedSemaphore) tryAcquire(weight int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters.Len() != 0 || s.size-s.held < weight {
		return false
	}
	s.held += weight
	return true
}

func (s *weightedSemaphore) release(weight int) {
	s.mu.Lock()
	s.held -= weight
	s.grantWaiters()
	s.mu.Unlock()
}

func (s *weightedSemaphore) grantWaiters() {
	for {
		front := s.waiters.Front()
		if front == nil {
			return
		}
		w := front.Value.(*semaphoreWaiter)
		if s.size-s.held < w.weight {
			return
		}
		s.held += w.weight
		s.waiters.Remove(front)
		close(w.ready)
	}
}
