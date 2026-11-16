package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

type fakeSender struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (s *fakeSender) SendToSavedMessages(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return s.err
}

func (s *fakeSender) lastCode(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.texts) == 0 {
		t.Fatal("nothing was sent to saved messages")
	}
	match := regexp.MustCompile(`butterDB pairing code: ([0-9A-Z]{5}-[0-9A-Z]{5})`).FindStringSubmatch(s.texts[len(s.texts)-1])
	if match == nil {
		t.Fatalf("no pairing code in %q", s.texts[len(s.texts)-1])
	}
	return match[1]
}

type pairingEnv struct {
	testEnv
	pairing *Pairing
	sender  *fakeSender
}

func newPairingEnv(t *testing.T) pairingEnv {
	t.Helper()
	env := newTestEnv(t)
	sender := &fakeSender{}
	pairing := NewPairing(env.repo, sender, env.tokens, env.clock, env.random)
	return pairingEnv{testEnv: env, pairing: pairing, sender: sender}
}

var laptop = adminv1.DeviceInfo{Name: "Office laptop", Platform: "windows/amd64", ClientVersion: "0.1.0", RemoteAddr: "203.0.113.9"}

func TestPairingCodeNormalisation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "canonical", input: "ABCDEFGHJK", want: "ABCDEFGHJK"},
		{name: "displayed with hyphen", input: "ABCDE-FGHJK", want: "ABCDEFGHJK"},
		{name: "lowercase and spaces", input: " abcde fghjk ", want: "ABCDEFGHJK"},
		{name: "letter O becomes zero", input: "OOOOO-OOOOO", want: "0000000000"},
		{name: "letters I and L become one", input: "IIIII-LLLLL", want: "1111111111"},
		{name: "mixed confusables", input: "o1il-0123xy", want: "01110123XY"},
		{name: "too short", input: "ABCDE", wantErr: true},
		{name: "too long", input: "ABCDEFGHJKM", wantErr: true},
		{name: "letter U is outside the alphabet", input: "UBCDEFGHJK", wantErr: true},
		{name: "punctuation", input: "ABCDE*FGHJ", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalisePairingCode(tc.input)
			if tc.wantErr {
				if !errors.Is(err, ErrPairingCodeInvalid) {
					t.Fatalf("error = %v, want ErrPairingCodeInvalid", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalisePairingCode(%q) = %q, %v, want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestPairingCodeSingleUse(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	expires, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !expires.Equal(testStart.Add(PairingCodeTTL)) {
		t.Fatalf("expires = %v", expires)
	}
	code := env.sender.lastCode(t)
	result, err := env.pairing.Complete(ctx, strings.ToLower(code), laptop)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Device.CreatedVia != string(adminrepo.CreatedViaPairingSavedMessages) || !result.Device.Current || result.Device.Name != laptop.Name {
		t.Fatalf("device = %+v", result.Device)
	}
	if _, err := ParseDeviceToken(result.DeviceToken); err != nil {
		t.Fatalf("issued token malformed: %v", err)
	}
	stored, err := env.repo.Device(ctx, result.Device.DeviceID)
	if err != nil || stored.TokenHash != HashSecret(KindDevice, result.DeviceToken) || stored.LastSeenIP != laptop.RemoteAddr {
		t.Fatalf("stored device = %+v, %v", stored, err)
	}
	if _, err := env.pairing.Complete(ctx, code, laptop); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("second Complete error = %v, want ErrPairingCodeInvalid", err)
	}
}

func TestPairingCodeExpiry(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		wantErr error
	}{
		{name: "one second before expiry", advance: PairingCodeTTL - time.Second},
		{name: "at expiry", advance: PairingCodeTTL, wantErr: ErrPairingCodeInvalid},
		{name: "long after expiry", advance: 3 * time.Hour, wantErr: ErrPairingCodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newPairingEnv(t)
			env.addDevice(t)
			if _, err := env.pairing.Request(context.Background(), DeliverySavedMessages, laptop); err != nil {
				t.Fatalf("Request: %v", err)
			}
			env.clock.Advance(tc.advance)
			_, err := env.pairing.Complete(context.Background(), env.sender.lastCode(t), laptop)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Complete error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFiveFailuresInvalidateCodes(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err != nil {
		t.Fatalf("Request: %v", err)
	}
	savedCode := env.sender.lastCode(t)
	hostCode, _, err := env.pairing.HostCode(ctx)
	if err != nil {
		t.Fatalf("HostCode: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := env.pairing.Complete(ctx, "ZZZZZ-ZZZZZ", laptop); !errors.Is(err, ErrPairingCodeInvalid) {
			t.Fatalf("failure %d error = %v", i, err)
		}
	}
	if _, err := env.pairing.Complete(ctx, hostCode, laptop); err != nil {
		t.Fatalf("a correct code after four failures must still work: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := env.pairing.Complete(ctx, "ZZZZZ-ZZZZZ", laptop); !errors.Is(err, ErrPairingCodeInvalid) {
			t.Fatalf("second round failure %d error = %v", i, err)
		}
	}
	if _, err := env.pairing.Complete(ctx, savedCode, laptop); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("saved messages code survived five failures: %v", err)
	}
}

func TestFailuresMustBeConsecutive(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			_, _ = env.pairing.Complete(ctx, "ZZZZZ-ZZZZZ", laptop)
		}
		if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err != nil {
			t.Fatalf("Request: %v", err)
		}
		if _, err := env.pairing.Complete(ctx, env.sender.lastCode(t), laptop); err != nil {
			t.Fatalf("round %d Complete: %v", round, err)
		}
	}
}

func TestMalformedCodeCountsAsFailure(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err != nil {
		t.Fatalf("Request: %v", err)
	}
	code := env.sender.lastCode(t)
	for i := 0; i < 5; i++ {
		if _, err := env.pairing.Complete(ctx, "short", laptop); !errors.Is(err, ErrPairingCodeInvalid) {
			t.Fatalf("error = %v", err)
		}
	}
	if _, err := env.pairing.Complete(ctx, code, laptop); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("code survived five malformed attempts: %v", err)
	}
}

func TestSavedMessagesSendFailureDeletesCode(t *testing.T) {
	tests := []struct {
		name    string
		sendErr error
	}{
		{name: "telegram down", sendErr: errors.New("telegram down")},
		{name: "not linked", sendErr: ErrPairingUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newPairingEnv(t)
			ctx := context.Background()
			env.addDevice(t)
			env.sender.err = tc.sendErr
			_, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop)
			if !errors.Is(err, tc.sendErr) {
				t.Fatalf("Request error = %v, want wrapping %v", err, tc.sendErr)
			}
			_, err = env.pairing.Complete(ctx, env.sender.lastCode(t), laptop)
			if !errors.Is(err, ErrPairingCodeInvalid) {
				t.Fatalf("code of a failed send still redeemable: %v", err)
			}
		})
	}
}

func TestSendFailureKeepsHostCode(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	hostCode, _, err := env.pairing.HostCode(ctx)
	if err != nil {
		t.Fatalf("HostCode: %v", err)
	}
	env.sender.err = errors.New("down")
	if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err == nil {
		t.Fatal("Request succeeded")
	}
	result, err := env.pairing.Complete(ctx, hostCode, laptop)
	if err != nil {
		t.Fatalf("host code lost by a saved messages failure: %v", err)
	}
	if result.Device.CreatedVia != string(adminrepo.CreatedViaPairingHost) {
		t.Fatalf("created via = %q", result.Device.CreatedVia)
	}
}

func TestSavedMessagesSendLimitedPerHour(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	for i := 0; i < 5; i++ {
		if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		env.clock.Advance(time.Minute)
	}
	_, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop)
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.Scope != ScopePairingRequest {
		t.Fatalf("sixth request error = %v, want pairing_request rate limit", err)
	}
	if want := 55 * time.Minute; limited.RetryAfter != want {
		t.Fatalf("RetryAfter = %v, want %v", limited.RetryAfter, want)
	}
	if len(env.sender.texts) != 5 {
		t.Fatalf("sends = %d, want 5", len(env.sender.texts))
	}
	env.clock.Advance(55 * time.Minute)
	if _, err := env.pairing.Request(ctx, DeliverySavedMessages, laptop); err != nil {
		t.Fatalf("request after the window: %v", err)
	}
}

func TestRequestPreconditions(t *testing.T) {
	tests := []struct {
		name     string
		claimed  bool
		delivery string
		wantErr  error
	}{
		{name: "claimed saved messages", claimed: true, delivery: DeliverySavedMessages},
		{name: "unclaimed server", claimed: false, delivery: DeliverySavedMessages, wantErr: ErrNotClaimed},
		{name: "host delivery is not requestable", claimed: true, delivery: DeliveryHost, wantErr: ErrUnsupportedDelivery},
		{name: "unknown delivery", claimed: true, delivery: "email", wantErr: ErrUnsupportedDelivery},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newPairingEnv(t)
			if tc.claimed {
				env.addDevice(t)
			}
			_, err := env.pairing.Request(context.Background(), tc.delivery, laptop)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && len(env.sender.texts) != 0 {
				t.Fatal("a message was sent despite the rejection")
			}
		})
	}
}

func TestCompleteOnUnclaimedServer(t *testing.T) {
	env := newPairingEnv(t)
	if _, err := env.pairing.Complete(context.Background(), "ABCDE-FGHJK", laptop); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("error = %v, want ErrNotClaimed", err)
	}
	if _, _, err := env.pairing.HostCode(context.Background()); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("HostCode error = %v, want ErrNotClaimed", err)
	}
}

func TestSavedMessagesText(t *testing.T) {
	env := newPairingEnv(t)
	env.addDevice(t)
	device := adminv1.DeviceInfo{Name: "Home \"desktop\"\n", Platform: "linux/amd64"}
	if _, err := env.pairing.Request(context.Background(), DeliverySavedMessages, device); err != nil {
		t.Fatalf("Request: %v", err)
	}
	lines := strings.Split(env.sender.texts[0], "\n")
	pattern := regexp.MustCompile(`^butterDB pairing code: [0-9A-Z]{5}-[0-9A-Z]{5}$`)
	if len(lines) != 4 || !pattern.MatchString(lines[0]) {
		t.Fatalf("text = %q", env.sender.texts[0])
	}
	want := []string{`Device: "Home  desktop  " (linux/amd64)`, "Expires: 2025-11-13 02:23 UTC", "If you did not request this, ignore it. Your server is still safe."}
	for i, line := range want {
		if lines[i+1] != line {
			t.Fatalf("line %d = %q, want %q", i+1, lines[i+1], line)
		}
	}
}

func TestHostCodeReplacesPreviousAndIsRedeemable(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	env.addDevice(t)
	first, _, err := env.pairing.HostCode(ctx)
	if err != nil {
		t.Fatalf("HostCode: %v", err)
	}
	second, expires, err := env.pairing.HostCode(ctx)
	if err != nil || !expires.Equal(testStart.Add(PairingCodeTTL)) {
		t.Fatalf("second HostCode = %v, %v", expires, err)
	}
	if first == second {
		t.Fatal("two host codes are identical")
	}
	if _, err := env.pairing.Complete(ctx, first, laptop); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("replaced host code error = %v", err)
	}
	if _, err := env.pairing.Complete(ctx, second, laptop); err != nil {
		t.Fatalf("current host code: %v", err)
	}
}

func TestPairingRequiresDependencies(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		name    string
		pairing *Pairing
	}{
		{name: "nil store", pairing: NewPairing(nil, &fakeSender{}, env.tokens, env.clock, env.random)},
		{name: "nil tokens", pairing: NewPairing(env.repo, &fakeSender{}, nil, env.clock, env.random)},
		{name: "nil clock", pairing: NewPairing(env.repo, &fakeSender{}, env.tokens, nil, env.random)},
		{name: "nil random", pairing: NewPairing(env.repo, &fakeSender{}, env.tokens, env.clock, nil)},
		{name: "nil sender", pairing: NewPairing(env.repo, nil, env.tokens, env.clock, env.random)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.pairing.Request(context.Background(), DeliverySavedMessages, laptop); err == nil {
				t.Fatal("Request succeeded")
			}
		})
	}
}

type staticControl struct {
	kind  string
	value string
	err   error
}

func (s staticControl) AdminToken(context.Context) (string, string, time.Time, error) {
	return s.kind, s.value, testStart.Add(time.Hour), s.err
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bdctl")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "admin.sock")
}

func startControl(t *testing.T, path string, h ControlHandler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeControlSocket(ctx, path, h) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := dialUnix(path); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket never appeared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("ServeControlSocket: %v", err)
		}
	}
}

func TestControlSocketMode0600(t *testing.T) {
	path := shortSocketPath(t)
	stop := startControl(t, path, staticControl{kind: KindPairingCode, value: "ABCDE-FGHJK"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("mode = %v, want socket 0600", info.Mode())
	}
	stop()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file after shutdown: %v", err)
	}
}

func TestControlSocketServesTokens(t *testing.T) {
	tests := []struct {
		name    string
		handler staticControl
		kind    string
		value   string
		wantErr error
	}{
		{name: "pairing code", handler: staticControl{kind: KindPairingCode, value: "ABCDE-FGHJK"}, kind: KindPairingCode, value: "ABCDE-FGHJK"},
		{name: "setup token", handler: staticControl{kind: KindSetupToken, value: "bdst1_" + strings.Repeat("a", 32)}, kind: KindSetupToken, value: "bdst1_" + strings.Repeat("a", 32)},
		{name: "handler failure hides the cause", handler: staticControl{err: errors.New("db password leaked")}, wantErr: ErrControlProtocol},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := shortSocketPath(t)
			stop := startControl(t, path, tc.handler)
			defer stop()
			kind, value, expires, err := RequestAdminToken(context.Background(), path)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if strings.Contains(err.Error(), "leaked") {
					t.Fatalf("error leaks handler detail: %v", err)
				}
				return
			}
			if kind != tc.kind || value != tc.value || !expires.Equal(testStart.Add(time.Hour)) {
				t.Fatalf("got %q %q %v", kind, value, expires)
			}
		})
	}
}

func TestControlSocketRejectsBadRequests(t *testing.T) {
	path := shortSocketPath(t)
	stop := startControl(t, path, staticControl{kind: KindPairingCode, value: "ABCDE-FGHJK"})
	defer stop()
	tests := []struct {
		name    string
		request string
		want    string
	}{
		{name: "wrong version", request: `{"version":2,"command":"admin_token"}`, want: `"ok":false`},
		{name: "unknown command", request: `{"version":1,"command":"shutdown"}`, want: `"ok":false`},
		{name: "not json", request: `hello`, want: `"ok":false`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rawControl(t, path, tc.request+"\n")
			if !strings.Contains(got, tc.want) || strings.Contains(got, "ABCDE") {
				t.Fatalf("response = %q", got)
			}
		})
	}
}

func rawControl(t *testing.T, path, request string) string {
	t.Helper()
	conn, err := dialUnix(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	return string(buf[:n])
}

func TestControlSocketReplacesStaleSocketFile(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	stop := startControl(t, path, staticControl{kind: KindPairingCode, value: "ABCDE-FGHJK"})
	defer stop()
	if _, _, _, err := RequestAdminToken(context.Background(), path); err != nil {
		t.Fatalf("RequestAdminToken: %v", err)
	}
}

type fakeSetupMinter struct {
	repo *adminrepo.Repository
	now  time.Time
}

func (m fakeSetupMinter) MintSetupTokenIfUnclaimed(ctx context.Context) (string, time.Time, error) {
	token := "bdst1_" + strings.Repeat("q", 32)
	expires := m.now.Add(24 * time.Hour)
	return token, expires, m.repo.ReplaceSetupToken(ctx, HashSecret(KindSetup, token), expires)
}

func TestAdminTokenWithoutRunningServer(t *testing.T) {
	env := newPairingEnv(t)
	ctx := context.Background()
	path := shortSocketPath(t)
	if _, _, _, err := RequestAdminToken(ctx, path); !errors.Is(err, ErrControlSocketUnreachable) {
		t.Fatalf("RequestAdminToken without a server error = %v, want ErrControlSocketUnreachable", err)
	}
	handler, err := NewAdminTokenHandler(AdminTokenDependencies{
		Claims: env.repo, Setup: fakeSetupMinter{repo: env.repo, now: testStart}, Pairing: env.pairing, Audit: env.repo, Clock: env.clock,
	})
	if err != nil {
		t.Fatalf("NewAdminTokenHandler: %v", err)
	}
	kind, token, expires, err := handler.AdminToken(ctx)
	if err != nil || kind != KindSetupToken || !strings.HasPrefix(token, "bdst1_") || !expires.Equal(testStart.Add(24*time.Hour)) {
		t.Fatalf("unclaimed AdminToken = %q %q %v %v", kind, token, expires, err)
	}
	claim := adminrepo.NewDevice{DeviceID: "aaaaaaaaaaaaaaaa", Name: "gui", Platform: "linux/amd64", TokenHash: HashSecret(KindDevice, "x"), CreatedAt: testStart}
	if err := env.repo.ConsumeSetupTokenAndAddDevice(ctx, HashSecret(KindSetup, token), testStart, claim); err != nil {
		t.Fatalf("claim with the issued token: %v", err)
	}
	kind, code, expires, err := handler.AdminToken(ctx)
	if err != nil || kind != KindPairingCode || !expires.Equal(testStart.Add(PairingCodeTTL)) {
		t.Fatalf("claimed AdminToken = %q %q %v %v", kind, code, expires, err)
	}
	result, err := env.pairing.Complete(ctx, code, laptop)
	if err != nil || result.Device.CreatedVia != string(adminrepo.CreatedViaPairingHost) {
		t.Fatalf("redeem host code = %+v, %v", result, err)
	}
	page, err := env.repo.PageAudit(ctx, adminrepo.AuditFilter{})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("audit = %+v, %v", page, err)
	}
	for i, wantAction := range []string{"pairing.code_issued", "setup.token_issued"} {
		e := page.Entries[i]
		if e.Action != wantAction || e.ActorType != "host" || e.Outcome != adminrepo.AuditOutcomeSuccess {
			t.Fatalf("audit entry %d = %+v", i, e)
		}
		if text := fmt.Sprint(e); strings.Contains(text, token) || strings.Contains(text, code) {
			t.Fatalf("audit entry %d contains a secret: %+v", i, e)
		}
	}
}

func TestAdminTokenHandlerRejectsNilDependencies(t *testing.T) {
	env := newPairingEnv(t)
	full := AdminTokenDependencies{Claims: env.repo, Setup: fakeSetupMinter{repo: env.repo}, Pairing: env.pairing, Audit: env.repo, Clock: env.clock}
	tests := []struct {
		name   string
		mutate func(d *AdminTokenDependencies)
	}{
		{name: "nil claims", mutate: func(d *AdminTokenDependencies) { d.Claims = nil }},
		{name: "nil setup", mutate: func(d *AdminTokenDependencies) { d.Setup = nil }},
		{name: "nil pairing", mutate: func(d *AdminTokenDependencies) { d.Pairing = nil }},
		{name: "nil audit", mutate: func(d *AdminTokenDependencies) { d.Audit = nil }},
		{name: "nil clock", mutate: func(d *AdminTokenDependencies) { d.Clock = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := full
			tc.mutate(&deps)
			if _, err := NewAdminTokenHandler(deps); err == nil {
				t.Fatal("NewAdminTokenHandler succeeded")
			}
		})
	}
}
