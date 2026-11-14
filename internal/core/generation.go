package core

import "sync"

type KeyGenerations struct {
	mu     sync.Mutex
	counts map[int64]uint64
}

func NewKeyGenerations() *KeyGenerations {
	return &KeyGenerations{counts: make(map[int64]uint64)}
}

func (g *KeyGenerations) Advance(bucketChannelID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[bucketChannelID]++
}

func (g *KeyGenerations) Generation(bucketChannelID int64) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[bucketChannelID]
}
