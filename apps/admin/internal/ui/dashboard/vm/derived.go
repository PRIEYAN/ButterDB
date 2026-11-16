package vm

import "time"

func (s State) CacheFraction() float64 {
	return fraction(s.Usage.BlockCache.UsedBytes, s.Usage.BlockCache.LimitBytes)
}

func (s State) BlockCacheOff() bool {
	return s.HasUsage && s.Usage.BlockCache.LimitBytes <= 0
}

func (s State) SpoolFraction() float64 {
	return fraction(s.Usage.Spool.UsedBytes, s.Usage.Spool.LimitBytes)
}

func (s State) ReplicaVisible() bool {
	state := s.Status.Replica.State
	return s.HasStatus && state != "" && state != ReplicaUnavailable
}

func (s State) FloodWaitActive(now time.Time) bool {
	until := s.Status.Telegram.FloodWaitUntil
	return s.HasStatus && until != nil && until.After(now)
}

func fraction(used, limit int64) float64 {
	if limit <= 0 || used <= 0 {
		return 0
	}
	if used >= limit {
		return 1
	}
	return float64(used) / float64(limit)
}
