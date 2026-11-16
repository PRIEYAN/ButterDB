package core

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeClaimRepository struct {
	consumeErr error
	hash       [32]byte
}

func (f *fakeClaimRepository) ClaimState(context.Context) (string, error) {
	return adminrepo.ClaimStateUnclaimed, nil
}

func (f *fakeClaimRepository) ReplaceSetupToken(_ context.Context, hash [32]byte, _ time.Time) error {
	f.hash = hash
	return nil
}

func (f *fakeClaimRepository) ConsumeSetupTokenAndAddDevice(context.Context, [32]byte, time.Time, adminrepo.NewDevice) error {
	return f.consumeErr
}

func TestClaimStoreTranslatesRepositoryErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"already claimed", adminrepo.ErrAlreadyClaimed, setup.ErrAlreadyClaimed},
		{"setup token invalid", adminrepo.ErrSetupTokenInvalid, setup.ErrSetupTokenInvalid},
		{"wrapped already claimed", errors.Join(errors.New("adminrepo: consume setup token"), adminrepo.ErrAlreadyClaimed), setup.ErrAlreadyClaimed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := claimStore{repo: &fakeClaimRepository{consumeErr: tc.err}}
			err := store.ConsumeSetupTokenAndAddDevice(context.Background(), [32]byte{}, time.Time{}, adminrepo.NewDevice{})
			if !errors.Is(err, tc.want) || !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want both %v and %v", err, tc.want, tc.err)
			}
		})
	}
	other := errors.New("disk full")
	store := claimStore{repo: &fakeClaimRepository{consumeErr: other}}
	if err := store.ConsumeSetupTokenAndAddDevice(context.Background(), [32]byte{}, time.Time{}, adminrepo.NewDevice{}); !errors.Is(err, other) {
		t.Fatalf("unrelated error was rewritten: %v", err)
	}
}

type countingRandom struct{ next byte }

func (c *countingRandom) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next++
	}
	return len(p), nil
}

type stubIssuer struct{}

func (stubIssuer) NewDeviceToken() (string, string, [32]byte, error) {
	return "", "", [32]byte{}, errors.New("not used")
}

func TestSetupTokenHashMatchesSessionHash(t *testing.T) {
	repo := &fakeClaimRepository{}
	service, err := setup.New(setup.Dependencies{
		Relay: offlineRelay{}, Claims: claimStore{repo: repo}, Tokens: stubIssuer{}, Clock: clock.NewFake(time.Unix(1763000000, 0)),
		Random: &countingRandom{}, Logger: quietLogger(), ServerID: "server",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := service.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil || token == "" {
		t.Fatalf("MintSetupTokenIfUnclaimed = %q, %v", token, err)
	}
	if want := session.HashSecret(session.KindSetup, token); repo.hash != want {
		t.Fatalf("setup stored %x, session hashes the same token to %x", repo.hash, want)
	}
	vector := session.HashSecret(session.KindSetup, "bdst1_"+strings.Repeat("a", 32))
	if got := hex.EncodeToString(vector[:]); got != "7473e863968ef3cb19b137d5e0f8efb2cf2240fc64aa835d9b0c44f7a37065ee" {
		t.Fatalf("setup hash vector = %s", got)
	}
}

func TestTranslateAdminError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		is   error
		code adminv1.Code
	}{
		{"claimed", setup.ErrAlreadyClaimed, handlers.ErrAlreadyClaimed, adminv1.CodeAlreadyClaimed},
		{"setup token", setup.ErrSetupTokenInvalid, handlers.ErrSetupTokenInvalid, adminv1.CodeSetupTokenInvalid},
		{"repo claimed", adminrepo.ErrAlreadyClaimed, handlers.ErrAlreadyClaimed, adminv1.CodeAlreadyClaimed},
		{"not claimed", session.ErrNotClaimed, handlers.ErrNotClaimed, adminv1.CodeNotClaimed},
		{"pairing code", session.ErrPairingCodeInvalid, handlers.ErrPairingCodeInvalid, adminv1.CodePairingCodeInvalid},
		{"repo pairing code", adminrepo.ErrPairingCodeInvalid, handlers.ErrPairingCodeInvalid, adminv1.CodePairingCodeInvalid},
		{"pairing unavailable", session.ErrPairingUnavailable, handlers.ErrPairingUnavailable, adminv1.CodePairingUnavailable},
		{"validation", &setup.ValidationError{Field: "device_name", Reason: "too short"}, nil, adminv1.CodeValidationFailed},
		{"unsupported delivery", session.ErrUnsupportedDelivery, nil, adminv1.CodeValidationFailed},
		{"rate limited", &session.RateLimitedError{Scope: session.ScopeClaim, RetryAfter: 90 * time.Second}, nil, adminv1.CodeRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translated := translateAdminError(tc.in)
			if tc.is != nil && !errors.Is(translated, tc.is) {
				t.Fatalf("translated error %v does not match %v", translated, tc.is)
			}
			if mapped := handlers.MapError(translated); mapped.Body.Code != tc.code {
				t.Fatalf("mapped code = %s, want %s", mapped.Body.Code, tc.code)
			}
		})
	}
	var limited *handlers.RateLimitedError
	if !errors.As(translateAdminError(&session.RateLimitedError{Scope: session.ScopeClaim, RetryAfter: time.Minute}), &limited) || limited.Scope != "claim" || limited.RetryAfter != time.Minute {
		t.Fatalf("rate limit was not carried over: %+v", limited)
	}
	plain := errors.New("boom")
	if translateAdminError(plain) != plain {
		t.Fatal("an unrelated error was rewritten")
	}
}

func TestPairingMethods(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		linked bool
		want   []string
	}{
		{"unclaimed", adminrepo.ClaimStateUnclaimed, false, []string{}},
		{"unclaimed but linked", adminrepo.ClaimStateUnclaimed, true, []string{}},
		{"claimed unlinked", adminrepo.ClaimStateClaimed, false, []string{"host"}},
		{"claimed linked", adminrepo.ClaimStateClaimed, true, []string{"host", "saved_messages"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pairingMethods(tc.state, tc.linked)
			if got == nil || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("pairingMethods = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestAuditCursorRoundTrip(t *testing.T) {
	cursor := adminrepo.AuditCursor{At: time.UnixMilli(1763000123456).UTC(), ID: 42}
	decoded, err := decodeAuditCursor(encodeAuditCursor(cursor))
	if err != nil || decoded != cursor {
		t.Fatalf("round trip = %+v, %v; want %+v", decoded, err, cursor)
	}
	if empty, err := decodeAuditCursor(""); err != nil || empty != (adminrepo.AuditCursor{}) {
		t.Fatalf("empty cursor = %+v, %v", empty, err)
	}
	for _, bad := range []string{"!!!", "bm90LWEtY3Vyc29y", "MS4w", "LTEuMQ", "YWJjLjE"} {
		var badRequest *handlers.BadRequestError
		if _, err := decodeAuditCursor(bad); !errors.As(err, &badRequest) || badRequest.Field != "cursor" {
			t.Fatalf("cursor %q = %v, want a bad request on cursor", bad, err)
		}
	}
}

type recordingAudit struct {
	mu      sync.Mutex
	entries []adminrepo.AuditEntry
}

func (r *recordingAudit) InsertAudit(_ context.Context, e adminrepo.AuditEntry) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return int64(len(r.entries)), nil
}

func (r *recordingAudit) PageAudit(context.Context, adminrepo.AuditFilter) (adminrepo.AuditPage, error) {
	return adminrepo.AuditPage{}, nil
}

func (r *recordingAudit) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func rejectingAuthenticator(code adminv1.Code) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer good" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"` + string(code) + `"}}`))
		})
	}
}

func TestAuthFailureAuditorRecordsOncePerAddressPerMinute(t *testing.T) {
	audit := &recordingAudit{}
	clk := clock.NewFake(time.Unix(1763000000, 0))
	auditor := newAuthFailureAuditor(audit, clk, quietLogger())
	handler := auditor.wrap(rejectingAuthenticator(adminv1.CodeDeviceRevoked))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func(remote, token string) int {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/status", nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		rec.Header().Set(adminv1.HeaderRequestID, "req-1")
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for range 4 {
		if code := send("203.0.113.9:1111", "bad"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d", code)
		}
	}
	if audit.count() != 1 {
		t.Fatalf("%d audit rows for four failures inside a minute, want 1", audit.count())
	}
	send("203.0.113.10:2222", "bad")
	if audit.count() != 2 {
		t.Fatalf("a second address was not audited: %d rows", audit.count())
	}
	if code := send("203.0.113.9:1111", "good"); code != http.StatusNoContent || audit.count() != 2 {
		t.Fatalf("an authenticated request changed the audit: status %d rows %d", code, audit.count())
	}
	clk.Advance(61 * time.Second)
	send("203.0.113.9:1111", "bad")
	entry := audit.entries[len(audit.entries)-1]
	if audit.count() != 3 || entry.Action != "auth.failure" || entry.ErrorCode != "device_revoked" || entry.RemoteAddr != "203.0.113.9" ||
		entry.Outcome != adminrepo.AuditOutcomeFailure || entry.ActorType != "anonymous" || entry.RequestID != "req-1" {
		t.Fatalf("rows %d, last entry %+v", audit.count(), entry)
	}
}

type stubRelay struct {
	offlineRelay
	state identity.State
}

func (s stubRelay) Status(context.Context) (identity.Snapshot, error) {
	return identity.Snapshot{State: s.state}, nil
}

type fixedReadiness struct{ state router.ServerState }

func (f fixedReadiness) State() router.ServerState { return f.state }

func TestBackendGateErrors(t *testing.T) {
	cases := []struct {
		name    string
		relay   identity.State
		publish *Backend
		want    error
	}{
		{"unlinked", identity.StateReady, nil, handlers.ErrTelegramNotLinked},
		{"linked without a runtime", identity.StateLinked, nil, handlers.ErrServerNotReady},
		{"initial sync running", identity.StateLinked, &Backend{Readiness: fixedReadiness{state: router.StateStarting}}, handlers.ErrServerNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slot := &BackendSlot{}
			if tc.publish != nil {
				slot.Publish(tc.publish)
			}
			_, err := backendGate{slot: slot, relay: stubRelay{state: tc.relay}}.backend(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("backend error = %v, want %v", err, tc.want)
			}
		})
	}
	slot := &BackendSlot{}
	want := &Backend{Readiness: fixedReadiness{state: router.StateDegraded}}
	slot.Publish(want)
	if got, err := (backendGate{slot: slot, relay: stubRelay{}}).backend(context.Background()); err != nil || got != want {
		t.Fatalf("degraded backend = %v, %v; reads must stay available", got, err)
	}
	slot.Clear()
	if slot.Current() != nil {
		t.Fatal("Clear left a backend published")
	}
}

type fakeDevices struct {
	devices map[string]adminrepo.Device
	rotated map[string][32]byte
}

func (f *fakeDevices) Device(_ context.Context, id string) (adminrepo.Device, error) {
	device, ok := f.devices[id]
	if !ok {
		return adminrepo.Device{}, adminrepo.ErrDeviceNotFound
	}
	return device, nil
}

func (f *fakeDevices) ListDevices(context.Context, time.Time) ([]adminrepo.Device, error) {
	return nil, nil
}

func (f *fakeDevices) ActiveDeviceCount(context.Context) (int, error) {
	active := 0
	for _, device := range f.devices {
		if device.RevokedAt.IsZero() {
			active++
		}
	}
	return active, nil
}

func (f *fakeDevices) RenameDevice(_ context.Context, id, name string) error {
	device, ok := f.devices[id]
	if !ok {
		return adminrepo.ErrDeviceNotFound
	}
	device.Name = name
	f.devices[id] = device
	return nil
}

func (f *fakeDevices) Rotate(_ context.Context, id string, hash [32]byte, _ time.Time) error {
	if _, ok := f.devices[id]; !ok {
		return adminrepo.ErrDeviceNotFound
	}
	f.rotated[id] = hash
	return nil
}

func (f *fakeDevices) Revoke(_ context.Context, id string, now time.Time) error {
	device := f.devices[id]
	device.RevokedAt = now
	f.devices[id] = device
	return nil
}

func newDeviceFixture(ids ...string) (*deviceService, *fakeDevices) {
	repo := &fakeDevices{devices: map[string]adminrepo.Device{}, rotated: map[string][32]byte{}}
	for _, id := range ids {
		repo.devices[id] = adminrepo.Device{DeviceID: id, Name: id}
	}
	return newDeviceService(repo, session.NewTokens(&countingRandom{}), clock.NewFake(time.Unix(1763000000, 0))), repo
}

func TestDeviceRotateKeepsDeviceIDAndStoresTheTokenHash(t *testing.T) {
	service, repo := newDeviceFixture("abcdefghijklmnop")
	_, token, err := service.Rotate(context.Background(), "abcdefghijklmnop")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	id, err := session.ParseDeviceToken(token)
	if err != nil || id != "abcdefghijklmnop" {
		t.Fatalf("rotated token %q parses to %q, %v", token, id, err)
	}
	if repo.rotated["abcdefghijklmnop"] != session.HashSecret(session.KindDevice, token) {
		t.Fatal("the stored hash is not the hash of the returned token")
	}
	if _, _, err := service.Rotate(context.Background(), "aaaaaaaaaaaaaaaa"); !errors.Is(err, handlers.ErrDeviceNotFound) {
		t.Fatalf("Rotate of an unknown device = %v", err)
	}
}

func TestDeviceRevokeEnforcesTheLastDeviceRule(t *testing.T) {
	cases := []struct {
		name        string
		devices     []string
		target      string
		confirmLast bool
		want        error
	}{
		{"one of two", []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}, "aaaaaaaaaaaaaaaa", false, nil},
		{"the only device", []string{"aaaaaaaaaaaaaaaa"}, "aaaaaaaaaaaaaaaa", false, handlers.ErrLastDevice},
		{"the only device confirmed", []string{"aaaaaaaaaaaaaaaa"}, "aaaaaaaaaaaaaaaa", true, nil},
		{"unknown device", []string{"aaaaaaaaaaaaaaaa"}, "cccccccccccccccc", true, handlers.ErrDeviceNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, repo := newDeviceFixture(tc.devices...)
			err := service.Revoke(context.Background(), tc.target, tc.confirmLast)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Revoke = %v, want %v", err, tc.want)
			}
			if revoked := !repo.devices[tc.target].RevokedAt.IsZero(); revoked != (tc.want == nil) {
				t.Fatalf("revoked = %v after error %v", revoked, err)
			}
		})
	}
}

func TestNewAdminRejectsMissingDependencies(t *testing.T) {
	if _, err := NewAdmin(AdminDependencies{}); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("NewAdmin(zero) = %v, want ErrMissingDependency", err)
	}
}

func TestDispositionFilename(t *testing.T) {
	cases := []struct{ key, want string }{
		{"cat.jpg", "cat.jpg"},
		{"albums/2026/cat.jpg", "cat.jpg"},
		{"a b.txt", "a%20b.txt"},
		{"café.txt", "caf%C3%A9.txt"},
		{"it's (1).txt", "it%27s%20%281%29.txt"},
		{"dir/", ""},
	}
	for _, tc := range cases {
		if got := dispositionFilename(tc.key); got != tc.want {
			t.Errorf("dispositionFilename(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestAdminAllowedHosts(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.AdminConfig
		want []string
	}{
		{"public listener checks nothing", config.AdminConfig{Listen: "0.0.0.0:9443", TLSMode: "self-signed"}, nil},
		{"loopback listener allows loopback names and hostnames", config.AdminConfig{Listen: "127.0.0.1:9443", TLSMode: "self-signed", Hostnames: []string{"admin.example.com"}}, []string{"localhost", "127.0.0.1", "::1", "admin.example.com"}},
		{"proxy mode always checks", config.AdminConfig{Listen: "0.0.0.0:9443", TLSMode: "proxy"}, []string{"localhost", "127.0.0.1", "::1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adminAllowedHosts(tc.cfg)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || (got == nil) != (tc.want == nil) {
				t.Fatalf("adminAllowedHosts = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestS3Endpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.S3Config
		want string
	}{
		{"public url wins", config.S3Config{PublicURL: "https://s3.example.com", Listen: "0.0.0.0:9000"}, "https://s3.example.com"},
		{"plain listener", config.S3Config{Listen: "127.0.0.1:9000"}, "http://127.0.0.1:9000"},
		{"tls files listener", config.S3Config{Listen: "127.0.0.1:9000", TLSMode: "files"}, "https://127.0.0.1:9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s3Endpoint(tc.cfg); got != tc.want {
				t.Fatalf("s3Endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

type recordingRelay struct {
	offlineRelay
	starts  int
	unlinks int
}

func (r *recordingRelay) Start(context.Context, string) (identity.Snapshot, error) {
	r.starts++
	return identity.Snapshot{State: identity.StateAwaitingCode}, nil
}

func (r *recordingRelay) Unlink(context.Context) (identity.Snapshot, error) {
	r.unlinks++
	return identity.Snapshot{State: identity.StateReady}, nil
}

func TestLimitedRelayAppliesTelegramLoginLimitAndClearsBackendOnUnlink(t *testing.T) {
	inner := &recordingRelay{}
	slot := &BackendSlot{}
	slot.Publish(&Backend{})
	clk := clock.NewFake(time.Unix(1763000000, 0))
	relay := &limitedRelay{inner: inner, limiter: session.NewLimiter(session.DefaultLimiterConfig(), clk), backend: slot}
	for i := 0; i < 5; i++ {
		if _, err := relay.Start(context.Background(), "+447700900134"); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	_, err := relay.Start(context.Background(), "+447700900134")
	var limited *handlers.RateLimitedError
	if !errors.As(err, &limited) || limited.Scope != "telegram_login" || limited.RetryAfter <= 0 || inner.starts != 5 {
		t.Fatalf("sixth start = %v after %d inner starts", err, inner.starts)
	}
	clk.Advance(13 * time.Minute)
	if _, err := relay.Start(context.Background(), "+447700900134"); err != nil {
		t.Fatalf("start after the bucket refilled: %v", err)
	}
	if _, err := relay.Unlink(context.Background()); err != nil || slot.Current() != nil {
		t.Fatalf("Unlink = %v, backend still published = %v", err, slot.Current() != nil)
	}
}

type adminSweepProbe struct {
	runs chan struct{}
}

func (c adminSweepProbe) RunOnce(ctx context.Context) error {
	select {
	case c.runs <- struct{}{}:
	case <-ctx.Done():
	}
	return nil
}

func TestAdminSweepRunsOnStartAndOncePerInterval(t *testing.T) {
	sweeper := adminSweepProbe{runs: make(chan struct{}, 8)}
	waits := make(chan time.Duration, 8)
	sleeper := flow.SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		select {
		case waits <- wait:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	component := newAdminSweep(sweeper, time.Hour, sleeper, quietLogger())
	if err := component.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case <-sweeper.runs:
		case <-time.After(5 * time.Second):
			t.Fatal("the sweeper did not run")
		}
		if wait := <-waits; wait != time.Hour {
			t.Fatalf("slept %v between sweeps, want an hour", wait)
		}
	}
	if err := component.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
