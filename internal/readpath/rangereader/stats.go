package rangereader

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

const statsLatencySamples = 128

type RangeStats struct {
	Fetched      int
	Cancelled    int
	CacheHits    int
	PeakInFlight int
	BlockP50     time.Duration
	BlockP95     time.Duration
}

type rangeStats struct {
	mu           sync.Mutex
	fetched      int
	cancelled    int
	cacheHits    int
	inFlight     int
	peakInFlight int
	latencies    []time.Duration
	next         int
}

func (s *rangeStats) hit() {
	s.mu.Lock()
	s.cacheHits++
	s.mu.Unlock()
}

func (s *rangeStats) begin() {
	s.mu.Lock()
	s.inFlight++
	s.peakInFlight = max(s.peakInFlight, s.inFlight)
	s.mu.Unlock()
}

func (s *rangeStats) end(err error) {
	s.mu.Lock()
	s.inFlight--
	switch {
	case err == nil:
		s.fetched++
	case errors.Is(err, context.Canceled):
		s.cancelled++
	}
	s.mu.Unlock()
}

func (s *rangeStats) observe(d time.Duration) {
	s.mu.Lock()
	if len(s.latencies) < statsLatencySamples {
		s.latencies = append(s.latencies, d)
	} else {
		s.latencies[s.next] = d
		s.next = (s.next + 1) % statsLatencySamples
	}
	s.mu.Unlock()
}

func (s *rangeStats) snapshot() RangeStats {
	s.mu.Lock()
	out := RangeStats{Fetched: s.fetched, Cancelled: s.cancelled, CacheHits: s.cacheHits, PeakInFlight: s.peakInFlight}
	sorted := slices.Clone(s.latencies)
	s.mu.Unlock()
	if len(sorted) == 0 {
		return out
	}
	slices.Sort(sorted)
	out.BlockP50 = sorted[len(sorted)/2]
	out.BlockP95 = sorted[min(len(sorted)*95/100, len(sorted)-1)]
	return out
}
