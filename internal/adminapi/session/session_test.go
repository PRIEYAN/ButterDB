package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

func TestDeviceTokenFormat(t *testing.T) {
	tokens := NewTokens(&countingReader{next: 7})
	pattern := regexp.MustCompile(`^bdt1_([a-z2-7]{16})_([a-z2-7]{52})$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		token, id, hash, err := tokens.NewDeviceToken()
		if err != nil {
			t.Fatalf("NewDeviceToken: %v", err)
		}
		match := pattern.FindStringSubmatch(token)
		if match == nil || match[1] != id {
			t.Fatalf("token %q does not match bdt1_<16>_<52> with id %q", token, id)
		}
		if hash != HashSecret(KindDevice, token) {
			t.Fatal("returned hash is not HashSecret(KindDevice, token)")
		}
		if seen[token] {
			t.Fatal("duplicate token")
		}
		seen[token] = true
		parsed, err := ParseDeviceToken(token)
		if err != nil || parsed != id {
			t.Fatalf("ParseDeviceToken = %q, %v", parsed, err)
		}
	}
	if _, _, _, err := NewTokens(nil).NewDeviceToken(); err == nil {
		t.Fatal("nil random accepted")
	}
}

func TestParseDeviceTokenRejectsMalformed(t *testing.T) {
	good := "bdt1_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)
	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{name: "well formed", token: good, ok: true},
		{name: "empty", token: ""},
		{name: "wrong prefix", token: "bdt2_" + good[5:]},
		{name: "short id", token: "bdt1_" + strings.Repeat("a", 15) + "_" + strings.Repeat("b", 52)},
		{name: "short secret", token: "bdt1_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 51)},
		{name: "uppercase", token: "bdt1_" + strings.Repeat("A", 16) + "_" + strings.Repeat("b", 52)},
		{name: "digit one outside alphabet", token: "bdt1_" + strings.Repeat("1", 16) + "_" + strings.Repeat("b", 52)},
		{name: "missing separator", token: "bdt1_" + strings.Repeat("a", 17) + strings.Repeat("b", 52)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDeviceToken(tc.token)
			if (err == nil) != tc.ok {
				t.Fatalf("ParseDeviceToken error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestSecretHashDomainSeparated(t *testing.T) {
	const value = "bdst1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	vectors := []struct {
		kind SecretKind
		in   string
		want string
	}{
		{KindSetup, value, "7473e863968ef3cb19b137d5e0f8efb2cf2240fc64aa835d9b0c44f7a37065ee"},
		{KindDevice, value, "58c4aa3a89dbf54abca9d37c3260b6c2fdf3ad882da071991754d1ce11988194"},
		{KindPairing, "ABCDEFGHJK", "b2c55aee0f272ce51835eda000950c70c26606bbd9b8750ab07b9ea4fdf0ccf6"},
	}
	for _, v := range vectors {
		t.Run(string(v.kind), func(t *testing.T) {
			h := HashSecret(v.kind, v.in)
			if got := hexString(h[:]); got != v.want {
				t.Fatalf("HashSecret(%s) = %s, want %s", v.kind, got, v.want)
			}
		})
	}
	setup, device, pairing := HashSecret(KindSetup, value), HashSecret(KindDevice, value), HashSecret(KindPairing, value)
	if setup == device || setup == pairing || device == pairing {
		t.Fatal("hashes of different kinds collide for the same value")
	}
	if HashSecret(KindDevice, value) != device {
		t.Fatal("HashSecret is not deterministic")
	}
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

func TestMiddlewareAuthenticatesBearerOnly(t *testing.T) {
	env := newTestEnv(t)
	token, deviceID := env.addDevice(t)
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			device, ok := DeviceFromContext(r.Context())
			if !ok || device.DeviceID != deviceID {
				t.Errorf("DeviceFromContext = %+v, %v", device, ok)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	tests := []struct {
		name   string
		header string
		query  string
		want   int
	}{
		{name: "valid bearer", header: "Bearer " + token, want: http.StatusNoContent},
		{name: "scheme case insensitive", header: "bearer " + token, want: http.StatusNoContent},
		{name: "no header", want: http.StatusUnauthorized},
		{name: "basic scheme", header: "Basic " + token, want: http.StatusUnauthorized},
		{name: "token in query only", query: "?token=" + token, want: http.StatusUnauthorized},
		{name: "unknown device", header: "Bearer bdt1_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52), want: http.StatusUnauthorized},
		{name: "right id wrong secret", header: "Bearer bdt1_" + deviceID + "_" + strings.Repeat("b", 52), want: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/v1/devices"+tc.query, nil)
			req.RemoteAddr = "198.51.100.1:5000"
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusUnauthorized {
				assertErrorCode(t, rec, adminv1.CodeUnauthenticated)
			}
		})
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want adminv1.Code) adminv1.ErrorBody {
	t.Helper()
	var envelope adminv1.ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body, err)
	}
	if envelope.Error.Code != want {
		t.Fatalf("error code = %q, want %q", envelope.Error.Code, want)
	}
	return envelope.Error
}

func TestRotateGraceWindowEndsOnFirstUse(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	oldToken, deviceID := env.addDevice(t)
	newToken, _, newHash, err := env.tokens.NewDeviceToken()
	if err != nil {
		t.Fatalf("NewDeviceToken: %v", err)
	}
	newToken = strings.Replace(newToken, newToken[5:21], deviceID, 1)
	newHash = HashSecret(KindDevice, newToken)
	if err := env.repo.Rotate(ctx, deviceID, newHash, env.clock.Now()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	env.clock.Advance(time.Minute)
	if rec := do(handler, oldToken, "198.51.100.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("old token inside grace status = %d, want 204", rec.Code)
	}
	if rec := do(handler, newToken, "198.51.100.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("new token status = %d, want 204", rec.Code)
	}
	if rec := do(handler, oldToken, "198.51.100.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after new token first used status = %d, want 401", rec.Code)
	}
	device, _ := env.repo.Device(ctx, deviceID)
	if device.HasPrevToken {
		t.Fatal("previous token still stored after first use of the new token")
	}
}

func TestRotateGraceWindowEndsAfterTenMinutes(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	oldToken, deviceID := env.addDevice(t)
	newToken := "bdt1_" + deviceID + "_" + strings.Repeat("c", 52)
	if err := env.repo.Rotate(ctx, deviceID, HashSecret(KindDevice, newToken), env.clock.Now()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	tests := []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{name: "just inside the grace window", advance: 9*time.Minute + 59*time.Second, want: http.StatusNoContent},
		{name: "exactly at ten minutes", advance: time.Second, want: http.StatusUnauthorized},
		{name: "well after ten minutes", advance: time.Hour, want: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env.clock.Advance(tc.advance)
			if rec := do(handler, oldToken, "198.51.100.1"); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestRevokedDeviceRejected(t *testing.T) {
	env := newTestEnv(t)
	token, deviceID := env.addDevice(t)
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	if rec := do(handler, token, "198.51.100.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("status before revoke = %d", rec.Code)
	}
	revokedAt := env.clock.Now().Add(time.Minute)
	if err := env.repo.Revoke(context.Background(), deviceID, revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	rec := do(handler, token, "198.51.100.1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status after revoke = %d, want 401", rec.Code)
	}
	body := assertErrorCode(t, rec, adminv1.CodeDeviceRevoked)
	if body.Details["revoked_at"] != formatMillis(revokedAt) {
		t.Fatalf("details = %v, want revoked_at %s", body.Details, formatMillis(revokedAt))
	}
	if rec := do(handler, token, "198.51.100.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("second request status = %d", rec.Code)
	}
}

func TestLastSeenCoalesced(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	token, deviceID := env.addDevice(t)
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	seenAt := func() time.Time {
		d, err := env.repo.Device(ctx, deviceID)
		if err != nil {
			t.Fatalf("Device: %v", err)
		}
		return d.LastSeenAt
	}
	steps := []struct {
		name     string
		advance  time.Duration
		addr     string
		wantSeen time.Time
	}{
		{name: "first request inside window of creation", advance: time.Second, addr: "198.51.100.1", wantSeen: testStart},
		{name: "past sixty seconds writes", advance: 60 * time.Second, addr: "198.51.100.2", wantSeen: testStart.Add(61 * time.Second)},
		{name: "ten seconds later is coalesced", advance: 10 * time.Second, addr: "198.51.100.3", wantSeen: testStart.Add(61 * time.Second)},
		{name: "fifty seconds later writes again", advance: 50 * time.Second, addr: "198.51.100.4", wantSeen: testStart.Add(121 * time.Second)},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			env.clock.Advance(step.advance)
			if rec := do(handler, token, step.addr); rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := seenAt(); !got.Equal(step.wantSeen) {
				t.Fatalf("last_seen_at = %v, want %v", got, step.wantSeen)
			}
		})
	}
	d, _ := env.repo.Device(ctx, deviceID)
	if d.LastSeenIP != "198.51.100.4" {
		t.Fatalf("last_seen_ip = %q, want 198.51.100.4", d.LastSeenIP)
	}
}

type auditSink struct {
	entries []adminrepo.AuditEntry
}

func (s *auditSink) InsertAudit(_ context.Context, e adminrepo.AuditEntry) (int64, error) {
	s.entries = append(s.entries, e)
	return int64(len(s.entries)), nil
}

func TestIdleRevokeSweeper(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	staleToken, staleID := env.addDevice(t)
	env.clock.Advance(100 * 24 * time.Hour)
	freshToken, freshID := env.addDevice(t)
	env.clock.Advance(100 * 24 * time.Hour)
	sink := &auditSink{}
	sweeper, err := NewSweeper(SweeperDependencies{Store: env.repo, Audit: sink, Clock: env.clock, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}
	if err := sweeper.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	stale, _ := env.repo.Device(ctx, staleID)
	fresh, _ := env.repo.Device(ctx, freshID)
	if stale.RevokedAt.IsZero() || stale.RevokedBy != adminrepo.RevokedByIdleSweeper {
		t.Fatalf("stale device not idle revoked: %+v", stale)
	}
	if !fresh.RevokedAt.IsZero() {
		t.Fatalf("fresh device revoked: %+v", fresh)
	}
	if len(sink.entries) != 1 || sink.entries[0].Action != "device.idle_revoke" || sink.entries[0].ActorType != "system" {
		t.Fatalf("audit = %+v", sink.entries)
	}
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	if rec := do(handler, staleToken, "198.51.100.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale token status = %d, want 401", rec.Code)
	}
	if rec := do(handler, freshToken, "198.51.100.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("fresh token status = %d, want 204", rec.Code)
	}
	if err := sweeper.RunOnce(ctx); err != nil || len(sink.entries) != 1 {
		t.Fatalf("second RunOnce = %v, audit rows %d, want no new audit", err, len(sink.entries))
	}
}

func TestSweeperRunStopsOnContext(t *testing.T) {
	env := newTestEnv(t)
	sweeper, err := NewSweeper(SweeperDependencies{Store: env.repo, Audit: &auditSink{}, Clock: env.clock, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx, ticks) }()
	ticks <- env.clock.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestNewSweeperRejectsNilDependencies(t *testing.T) {
	env := newTestEnv(t)
	full := SweeperDependencies{Store: env.repo, Audit: &auditSink{}, Clock: env.clock, Logger: discardLogger()}
	tests := []struct {
		name   string
		mutate func(d *SweeperDependencies)
	}{
		{name: "nil store", mutate: func(d *SweeperDependencies) { d.Store = nil }},
		{name: "nil audit", mutate: func(d *SweeperDependencies) { d.Audit = nil }},
		{name: "nil clock", mutate: func(d *SweeperDependencies) { d.Clock = nil }},
		{name: "nil logger", mutate: func(d *SweeperDependencies) { d.Logger = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := full
			tc.mutate(&deps)
			if _, err := NewSweeper(deps); err == nil {
				t.Fatal("NewSweeper succeeded")
			}
		})
	}
}

func TestRateLimiterPerScope(t *testing.T) {
	env := newTestEnv(t)
	limiter := NewLimiter(DefaultLimiterConfig(), env.clock)
	tests := []struct {
		name  string
		scope Scope
		key   string
		burst int
	}{
		{name: "claim per address", scope: ScopeClaim, key: "198.51.100.1", burst: 5},
		{name: "pairing request server wide", scope: ScopePairingRequest, key: ServerWideKey, burst: 5},
		{name: "pairing request per address", scope: ScopePairingRequestAddr, key: "198.51.100.1", burst: 3},
		{name: "pairing complete per address", scope: ScopePairingComplete, key: "198.51.100.1", burst: 10},
		{name: "telegram login server wide", scope: ScopeTelegramLogin, key: ServerWideKey, burst: 5},
		{name: "telegram app server wide", scope: ScopeTelegramApp, key: ServerWideKey, burst: 10},
		{name: "device per device", scope: ScopeDevice, key: "dev", burst: 100},
		{name: "unauthenticated per address", scope: ScopeUnauthenticated, key: "198.51.100.1", burst: 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < tc.burst; i++ {
				if err := limiter.Take(tc.scope, tc.key); err != nil {
					t.Fatalf("take %d of %d: %v", i+1, tc.burst, err)
				}
			}
			err := limiter.Take(tc.scope, tc.key)
			var limited *RateLimitedError
			if !errors.As(err, &limited) || !errors.Is(err, ErrRateLimited) {
				t.Fatalf("take beyond burst error = %v, want RateLimitedError", err)
			}
			if limited.Scope != tc.scope || limited.RetryAfter <= 0 {
				t.Fatalf("limited = %+v", limited)
			}
			if err := limiter.Take(tc.scope, "another-key"); tc.key != ServerWideKey && err != nil {
				t.Fatalf("a different key shares the bucket: %v", err)
			}
			env.clock.Advance(limited.RetryAfter)
			if err := limiter.Take(tc.scope, tc.key); err != nil {
				t.Fatalf("take after retry-after: %v", err)
			}
		})
	}
	if err := limiter.Take(ScopeTelegramSubmit, "x"); err != nil {
		t.Fatalf("unconfigured scope limited: %v", err)
	}
}

func TestRateLimiterScopesAreIndependent(t *testing.T) {
	env := newTestEnv(t)
	limiter := NewLimiter(DefaultLimiterConfig(), env.clock)
	for i := 0; i < 5; i++ {
		_ = limiter.Take(ScopeClaim, "a")
	}
	if err := limiter.Take(ScopeClaim, "a"); err == nil {
		t.Fatal("claim not limited")
	}
	if err := limiter.Take(ScopePairingComplete, "a"); err != nil {
		t.Fatalf("pairing_complete limited by claim: %v", err)
	}
}

func TestRateLimiterEvictsFullBuckets(t *testing.T) {
	env := newTestEnv(t)
	cfg := DefaultLimiterConfig()
	cfg.MaxKeys = 2
	limiter := NewLimiter(cfg, env.clock)
	for _, key := range []string{"a", "b"} {
		_ = limiter.Take(ScopeClaim, key)
	}
	env.clock.Advance(time.Hour)
	_ = limiter.Take(ScopeClaim, "c")
	limiter.mu.Lock()
	size := len(limiter.buckets[ScopeClaim])
	limiter.mu.Unlock()
	if size != 1 {
		t.Fatalf("bucket count = %d, want 1 after eviction of refilled buckets", size)
	}
}

func TestAuthFailureBlock(t *testing.T) {
	env := newTestEnv(t)
	token, _ := env.addDevice(t)
	limiter := NewLimiter(DefaultLimiterConfig(), env.clock)
	handler := env.authenticator(limiter).Middleware(okHandler)
	badToken := "bdt1_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)
	for i := 0; i < 20; i++ {
		if rec := do(handler, badToken, "198.51.100.9"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i+1, rec.Code)
		}
	}
	rec := do(handler, token, "198.51.100.9")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked address with a valid token status = %d, want 429", rec.Code)
	}
	body := assertErrorCode(t, rec, adminv1.CodeRateLimited)
	if body.Details["scope"] != string(ScopeAuthFailure) || rec.Header().Get("Retry-After") != "900" || body.RetryAfterSeconds != 900 {
		t.Fatalf("429 body = %+v, Retry-After %q", body, rec.Header().Get("Retry-After"))
	}
	if rec := do(handler, token, "198.51.100.10"); rec.Code != http.StatusNoContent {
		t.Fatalf("a different address status = %d, want 204", rec.Code)
	}
	env.clock.Advance(15*time.Minute + time.Second)
	if rec := do(handler, token, "198.51.100.9"); rec.Code != http.StatusNoContent {
		t.Fatalf("status after block expiry = %d, want 204", rec.Code)
	}
}

func TestAuthFailureWindowResets(t *testing.T) {
	env := newTestEnv(t)
	token, _ := env.addDevice(t)
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	for i := 0; i < 19; i++ {
		do(handler, "", "198.51.100.9")
	}
	env.clock.Advance(11 * time.Minute)
	for i := 0; i < 19; i++ {
		do(handler, "", "198.51.100.9")
	}
	if rec := do(handler, token, "198.51.100.9"); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 because the failure window reset", rec.Code)
	}
}

func TestDeviceScopeRateLimit(t *testing.T) {
	env := newTestEnv(t)
	token, _ := env.addDevice(t)
	handler := env.authenticator(NewLimiter(DefaultLimiterConfig(), env.clock)).Middleware(okHandler)
	for i := 0; i < 100; i++ {
		if rec := do(handler, token, "198.51.100.1"); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d", i+1, rec.Code)
		}
	}
	rec := do(handler, token, "198.51.100.1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if body := assertErrorCode(t, rec, adminv1.CodeRateLimited); body.Details["scope"] != string(ScopeDevice) {
		t.Fatalf("details = %v", body.Details)
	}
}

func TestResolveClientAddr(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{name: "no proxy", remote: "203.0.113.5:1234", want: "203.0.113.5"},
		{name: "untrusted peer header ignored", remote: "203.0.113.5:1234", xff: []string{"1.2.3.4"}, want: "203.0.113.5"},
		{name: "trusted peer honours header", remote: "127.0.0.1:1234", xff: []string{"1.2.3.4"}, want: "1.2.3.4"},
		{name: "rightmost untrusted entry wins", remote: "127.0.0.1:1234", xff: []string{"9.9.9.9, 1.2.3.4, 127.0.0.1"}, want: "1.2.3.4"},
		{name: "multiple header lines", remote: "[::1]:1234", xff: []string{"9.9.9.9", "1.2.3.4"}, want: "1.2.3.4"},
		{name: "garbage header falls back to peer", remote: "127.0.0.1:1234", xff: []string{"not-an-ip"}, want: "127.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := ResolveClientAddr(req, trusted); got != tc.want {
				t.Fatalf("ResolveClientAddr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMiddlewareUsesContextClientAddr(t *testing.T) {
	env := newTestEnv(t)
	limiter := NewLimiter(DefaultLimiterConfig(), env.clock)
	handler := env.authenticator(limiter).Middleware(okHandler)
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:1"
		req = req.WithContext(ContextWithClientAddr(req.Context(), "203.0.113.50"))
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if _, blocked := limiter.Blocked("203.0.113.50"); !blocked {
		t.Fatal("resolved client address not blocked")
	}
	if _, blocked := limiter.Blocked("127.0.0.1"); blocked {
		t.Fatal("peer address blocked instead of resolved address")
	}
}
