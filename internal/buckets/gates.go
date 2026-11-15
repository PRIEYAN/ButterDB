package buckets

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/semaphore"
)

const exclusiveWeight = 1 << 30

type WriteGates struct {
	mu    sync.Mutex
	gates map[int64]*semaphore.Weighted
}

func NewWriteGates() *WriteGates {
	return &WriteGates{gates: map[int64]*semaphore.Weighted{}}
}

func (g *WriteGates) AcquireShared(ctx context.Context, bucketChannelID int64) (func(), error) {
	return g.acquire(ctx, bucketChannelID, 1)
}

func (g *WriteGates) AcquireExclusive(ctx context.Context, bucketChannelID int64) (func(), error) {
	return g.acquire(ctx, bucketChannelID, exclusiveWeight)
}

func (g *WriteGates) acquire(ctx context.Context, bucketChannelID, weight int64) (func(), error) {
	gate := g.gateFor(bucketChannelID)
	if err := gate.Acquire(ctx, weight); err != nil {
		return nil, fmt.Errorf("buckets: write gate for channel %d: %w", bucketChannelID, err)
	}
	var once sync.Once
	return func() { once.Do(func() { gate.Release(weight) }) }, nil
}

func (g *WriteGates) gateFor(bucketChannelID int64) *semaphore.Weighted {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate, ok := g.gates[bucketChannelID]
	if !ok {
		gate = semaphore.NewWeighted(exclusiveWeight)
		g.gates[bucketChannelID] = gate
	}
	return gate
}
