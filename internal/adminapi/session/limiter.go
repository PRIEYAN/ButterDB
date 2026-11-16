package session

import (
	"math"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

type Scope string

const (
	ScopeClaim              Scope = "claim"
	ScopePairingRequest     Scope = "pairing_request"
	ScopePairingRequestAddr Scope = "pairing_request_addr"
	ScopePairingComplete    Scope = "pairing_complete"
	ScopeAuthFailure        Scope = "auth_failure"
	ScopeTelegramLogin      Scope = "telegram_login"
	ScopeTelegramApp        Scope = "telegram_app"
	ScopeTelegramSubmit     Scope = "telegram_submit"
	ScopeDevice             Scope = "device"
	ScopeUnauthenticated    Scope = "unauthenticated"
)

const (
	defaultMaxKeys    = 10000
	ServerWideKey     = ""
	failureMaxDefault = 20
)

type Rule struct {
	Burst    int
	Interval time.Duration
}

type FailureRule struct {
	Max    int
	Window time.Duration
	Block  time.Duration
}

type LimiterConfig struct {
	Rules       map[Scope]Rule
	AuthFailure FailureRule
	MaxKeys     int
}

func DefaultLimiterConfig() LimiterConfig {
	return LimiterConfig{
		Rules: map[Scope]Rule{
			ScopeClaim:              {Burst: 5, Interval: 3 * time.Minute},
			ScopePairingRequest:     {Burst: 5, Interval: 12 * time.Minute},
			ScopePairingRequestAddr: {Burst: 3, Interval: 20 * time.Minute},
			ScopePairingComplete:    {Burst: 10, Interval: 90 * time.Second},
			ScopeTelegramLogin:      {Burst: 5, Interval: 12 * time.Minute},
			ScopeTelegramApp:        {Burst: 10, Interval: 6 * time.Minute},
			ScopeDevice:             {Burst: 100, Interval: 20 * time.Millisecond},
			ScopeUnauthenticated:    {Burst: 20, Interval: 100 * time.Millisecond},
		},
		AuthFailure: FailureRule{Max: failureMaxDefault, Window: 10 * time.Minute, Block: 15 * time.Minute},
		MaxKeys:     defaultMaxKeys,
	}
}

type bucketState struct {
	tokens float64
	last   time.Time
}

type failureState struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

type Limiter struct {
	mu       sync.Mutex
	clock    clock.Clock
	rules    map[Scope]Rule
	failure  FailureRule
	maxKeys  int
	buckets  map[Scope]map[string]*bucketState
	failures map[string]*failureState
}

func NewLimiter(cfg LimiterConfig, clk clock.Clock) *Limiter {
	rules := make(map[Scope]Rule, len(cfg.Rules))
	for scope, rule := range cfg.Rules {
		rules[scope] = rule
	}
	maxKeys := cfg.MaxKeys
	if maxKeys <= 0 {
		maxKeys = defaultMaxKeys
	}
	return &Limiter{
		clock:    clk,
		rules:    rules,
		failure:  cfg.AuthFailure,
		maxKeys:  maxKeys,
		buckets:  make(map[Scope]map[string]*bucketState),
		failures: make(map[string]*failureState),
	}
}

func (l *Limiter) Take(scope Scope, key string) error {
	retryAfter, ok := l.Allow(scope, key)
	if ok {
		return nil
	}
	return &RateLimitedError{Scope: scope, RetryAfter: retryAfter}
}

func (l *Limiter) Allow(scope Scope, key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rule, configured := l.rules[scope]
	if !configured || rule.Burst <= 0 || rule.Interval <= 0 {
		return 0, true
	}
	now := l.clock.Now()
	state := l.stateFor(scope, key, rule, now)
	refill(state, rule, now)
	if state.tokens >= 1 {
		state.tokens--
		return 0, true
	}
	missing := 1 - state.tokens
	return time.Duration(math.Ceil(missing * float64(rule.Interval))), false
}

func (l *Limiter) stateFor(scope Scope, key string, rule Rule, now time.Time) *bucketState {
	perScope, ok := l.buckets[scope]
	if !ok {
		perScope = make(map[string]*bucketState)
		l.buckets[scope] = perScope
	}
	state, ok := perScope[key]
	if ok {
		return state
	}
	if len(perScope) >= l.maxKeys {
		dropFullBuckets(perScope, rule, now)
	}
	state = &bucketState{tokens: float64(rule.Burst), last: now}
	perScope[key] = state
	return state
}

func dropFullBuckets(perScope map[string]*bucketState, rule Rule, now time.Time) {
	for key, state := range perScope {
		refill(state, rule, now)
		if state.tokens >= float64(rule.Burst) {
			delete(perScope, key)
		}
	}
}

func refill(state *bucketState, rule Rule, now time.Time) {
	elapsed := now.Sub(state.last)
	if elapsed > 0 {
		state.tokens = math.Min(float64(rule.Burst), state.tokens+float64(elapsed)/float64(rule.Interval))
	}
	state.last = now
}

func (l *Limiter) Blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.failures[key]
	if !ok {
		return 0, false
	}
	now := l.clock.Now()
	if now.Before(state.blockedUntil) {
		return state.blockedUntil.Sub(now), true
	}
	return 0, false
}

func (l *Limiter) RecordAuthFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failure.Max <= 0 {
		return
	}
	now := l.clock.Now()
	state, ok := l.failures[key]
	if !ok {
		l.dropIdleFailures(now)
		state = &failureState{windowStart: now}
		l.failures[key] = state
	}
	if now.Before(state.blockedUntil) {
		return
	}
	if now.Sub(state.windowStart) > l.failure.Window {
		state.count = 0
		state.windowStart = now
	}
	state.count++
	if state.count >= l.failure.Max {
		state.blockedUntil = now.Add(l.failure.Block)
		state.count = 0
		state.windowStart = now
	}
}

func (l *Limiter) dropIdleFailures(now time.Time) {
	if len(l.failures) < l.maxKeys {
		return
	}
	for key, state := range l.failures {
		if !now.Before(state.blockedUntil) && now.Sub(state.windowStart) > l.failure.Window {
			delete(l.failures, key)
		}
	}
}
