package scan

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
)

var _ syncer.ChannelGate = (*Gate)(nil)

type Gate struct {
	mu    sync.Mutex
	locks map[int64]*semaphore.Weighted
}

func NewGate() *Gate {
	return &Gate{locks: make(map[int64]*semaphore.Weighted)}
}

func (g *Gate) Acquire(ctx context.Context, channelID int64) (func(), error) {
	lock := g.lockFor(channelID)
	if err := lock.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { lock.Release(1) }) }, nil
}

// A gate is kept for the process lifetime: dropping one while callers hold its pointer would admit two scans into one channel.
func (g *Gate) lockFor(channelID int64) *semaphore.Weighted {
	g.mu.Lock()
	defer g.mu.Unlock()
	if lock, ok := g.locks[channelID]; ok {
		return lock
	}
	lock := semaphore.NewWeighted(1)
	g.locks[channelID] = lock
	return lock
}
