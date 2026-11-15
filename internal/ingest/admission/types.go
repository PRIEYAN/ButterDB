package admission

import "time"

type Budget struct {
	Deadline time.Time
}

type SlowDownError struct {
	RetryAfter time.Duration
	Reason     string
}

type Config struct {
	MaxConcurrentPuts       int
	MaxConcurrentPutsPerKey int
}
