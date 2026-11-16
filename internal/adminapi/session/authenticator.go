package session

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	LastSeenCoalesce = 60 * time.Second
	requestIDHeader  = "X-Request-Id"
)

type Authenticator struct {
	store  DeviceStore
	limits *Limiter
	clock  clock.Clock

	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func NewAuthenticator(store DeviceStore, limits *Limiter, clock clock.Clock) *Authenticator {
	return &Authenticator{store: store, limits: limits, clock: clock, lastSeen: make(map[string]time.Time)}
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := clientAddr(r)
		if retryAfter, blocked := a.limits.Blocked(addr); blocked {
			writeRateLimited(w, ScopeAuthFailure, retryAfter)
			return
		}
		device, ok := a.authenticate(w, r, addr)
		if !ok {
			return
		}
		if retryAfter, allowed := a.limits.Allow(ScopeDevice, device.DeviceID); !allowed {
			writeRateLimited(w, ScopeDevice, retryAfter)
			return
		}
		a.touchSeen(r.Context(), device, addr)
		ctx := ContextWithDevice(ContextWithClientAddr(r.Context(), addr), device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Authenticator) authenticate(w http.ResponseWriter, r *http.Request, addr string) (adminrepo.Device, bool) {
	token, ok := bearerToken(r)
	if !ok {
		a.rejectUnauthenticated(w, addr)
		return adminrepo.Device{}, false
	}
	deviceID, err := ParseDeviceToken(token)
	if err != nil {
		a.rejectUnauthenticated(w, addr)
		return adminrepo.Device{}, false
	}
	device, err := a.store.Device(r.Context(), deviceID)
	if errors.Is(err, adminrepo.ErrDeviceNotFound) {
		a.rejectUnauthenticated(w, addr)
		return adminrepo.Device{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, adminv1.CodeInternal, "Internal error.", nil, 0, false)
		return adminrepo.Device{}, false
	}
	now := a.clock.Now()
	switch matchToken(device, token, now) {
	case tokenRejected:
		a.rejectUnauthenticated(w, addr)
		return adminrepo.Device{}, false
	case tokenCurrentWithGrace:
		_ = a.store.MarkNewTokenUsed(r.Context(), device.DeviceID)
	}
	if !device.RevokedAt.IsZero() {
		details := map[string]any{"revoked_at": formatMillis(device.RevokedAt)}
		writeError(w, http.StatusUnauthorized, adminv1.CodeDeviceRevoked, "Device revoked.", details, 0, false)
		return adminrepo.Device{}, false
	}
	return device, true
}

type tokenMatch int

const (
	tokenRejected tokenMatch = iota
	tokenCurrent
	tokenCurrentWithGrace
	tokenPrevious
)

func matchToken(device adminrepo.Device, token string, now time.Time) tokenMatch {
	presented := HashSecret(KindDevice, token)
	current := subtle.ConstantTimeCompare(presented[:], device.TokenHash[:]) == 1
	previous := subtle.ConstantTimeCompare(presented[:], device.PrevTokenHash[:]) == 1
	switch {
	case current && device.HasPrevToken:
		return tokenCurrentWithGrace
	case current:
		return tokenCurrent
	case previous && device.HasPrevToken && now.Before(device.PrevValidUntil):
		return tokenPrevious
	}
	return tokenRejected
}

func (a *Authenticator) rejectUnauthenticated(w http.ResponseWriter, addr string) {
	a.limits.RecordAuthFailure(addr)
	writeError(w, http.StatusUnauthorized, adminv1.CodeUnauthenticated, "Missing or invalid device token.", nil, 0, false)
}

func (a *Authenticator) touchSeen(ctx context.Context, device adminrepo.Device, addr string) {
	now := a.clock.Now()
	a.mu.Lock()
	last, seen := a.lastSeen[device.DeviceID]
	if !seen {
		last = device.LastSeenAt
	}
	if now.Sub(last) < LastSeenCoalesce {
		a.mu.Unlock()
		return
	}
	a.lastSeen[device.DeviceID] = now
	a.mu.Unlock()
	_ = a.store.TouchSeen(ctx, device.DeviceID, addr, now)
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func writeRateLimited(w http.ResponseWriter, scope Scope, retryAfter time.Duration) {
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	details := map[string]any{"scope": string(scope)}
	writeError(w, http.StatusTooManyRequests, adminv1.CodeRateLimited, "Too many requests.", details, seconds, true)
}

func writeError(w http.ResponseWriter, status int, code adminv1.Code, message string, details map[string]any, retryAfterSeconds int, retryable bool) {
	envelope := adminv1.ErrorEnvelope{Error: adminv1.ErrorBody{
		Code:              code,
		Message:           message,
		Retryable:         retryable,
		RetryAfterSeconds: retryAfterSeconds,
		Details:           details,
		RequestID:         w.Header().Get(requestIDHeader),
	}}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}
