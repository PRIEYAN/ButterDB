package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

func laptop() adminv1.DeviceInfo {
	return adminv1.DeviceInfo{Name: "Office laptop", Platform: "windows/amd64", ClientVersion: "0.1.0", RemoteAddr: "203.0.113.7"}
}

func TestSetupTokenHashMatchesSpecConstruction(t *testing.T) {
	got := hashSetupToken("bdst1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	want := "7473e863968ef3cb19b137d5e0f8efb2cf2240fc64aa835d9b0c44f7a37065ee"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("hash = %x, want %s", got, want)
	}
	other := hashSetupToken("bdst1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
	if other == got {
		t.Fatal("different tokens hash equal")
	}
}

func TestSetupTokenFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"valid", "bdst1_abcdefghijklmnopqrstuvwxyz234567", true},
		{"wrong prefix", "bdst2_abcdefghijklmnopqrstuvwxyz234567", false},
		{"too short", "bdst1_abcdefghijklmnopqrstuvwxyz23456", false},
		{"too long", "bdst1_abcdefghijklmnopqrstuvwxyz2345678", false},
		{"uppercase", "bdst1_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", false},
		{"digit outside base32", "bdst1_abcdefghijklmnopqrstuvwxyz234561", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wellFormedSetupToken(tc.token); got != tc.want {
				t.Fatalf("wellFormedSetupToken(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestSetupTokenPrintedOnce(t *testing.T) {
	f := newClaimFixture(t)
	token, expiresAt, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !wellFormedSetupToken(token) {
		t.Fatalf("token %q is not well formed", token)
	}
	if want := f.clock.Now().Add(24 * time.Hour); !expiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", expiresAt, want)
	}
	if _, err := f.svc.Claim(context.Background(), token, laptop()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background()); err != nil {
		t.Fatalf("mint after claim: %v", err)
	}
	logs := f.logs.String()
	if got := strings.Count(logs, token); got != 1 {
		t.Fatalf("token appears %d times in logs, want 1: %s", got, logs)
	}
	for _, field := range []string{`"level":"WARN"`, `"msg":"setup token"`, `"setup_token":`, `"expires_at":`} {
		if !strings.Contains(logs, field) {
			t.Fatalf("log line misses %s: %s", field, logs)
		}
	}
	if strings.Count(logs, "\n") != 2 {
		t.Fatalf("want the setup token line plus the claimed line, got: %s", logs)
	}
}

func TestSetupTokenNotMintedWhenClaimed(t *testing.T) {
	store := &memClaimStore{devices: []adminrepo.NewDevice{{DeviceID: "existing"}}}
	f := newClaimFixtureOn(t, store, &sequenceReader{})
	token, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil || token != "" {
		t.Fatalf("mint on claimed server = %q, %v; want empty token and nil error", token, err)
	}
	if store.replaced != 0 || f.logs.Len() != 0 {
		t.Fatalf("claimed server stored or logged a token: replaced=%d logs=%q", store.replaced, f.logs.String())
	}
}

func TestSetupTokenReplacedOnRestart(t *testing.T) {
	store := &memClaimStore{}
	random := &sequenceReader{}
	first := newClaimFixtureOn(t, store, random)
	oldToken, _, err := first.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("first mint: %v", err)
	}
	restarted := newClaimFixtureOn(t, store, random)
	newToken, _, err := restarted.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if oldToken == newToken {
		t.Fatal("restart minted the same token")
	}
	if store.replaced != 2 {
		t.Fatalf("replace calls = %d, want 2", store.replaced)
	}
	if _, err := restarted.svc.Claim(context.Background(), oldToken, laptop()); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatalf("claim with replaced token error = %v, want ErrSetupTokenInvalid", err)
	}
	if _, err := restarted.svc.Claim(context.Background(), newToken, laptop()); err != nil {
		t.Fatalf("claim with current token: %v", err)
	}
}

func TestClaimConsumesToken(t *testing.T) {
	f := newClaimFixture(t)
	token, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	result, err := f.svc.Claim(context.Background(), token, laptop())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if f.store.tokenStored() {
		t.Fatal("setup token still stored after claim")
	}
	if f.store.deviceCount() != 1 {
		t.Fatalf("devices = %d, want 1", f.store.deviceCount())
	}
	dev := f.store.devices[0]
	if dev.CreatedVia != adminrepo.CreatedViaSetup || dev.Name != "Office laptop" || dev.LastSeenIP != "203.0.113.7" || !dev.CreatedAt.Equal(f.clock.Now()) {
		t.Fatalf("stored device = %+v", dev)
	}
	if result.ServerID != testServerID || result.DeviceToken == "" || result.Device.DeviceID != dev.DeviceID {
		t.Fatalf("result = %+v", result)
	}
	if !result.Device.Current || result.Device.CreatedVia != "setup" || result.Device.RevokedAt != nil {
		t.Fatalf("result device = %+v", result.Device)
	}
	if !result.Device.TokenIssuedAt.Equal(f.clock.Now()) || !result.Device.CreatedAt.Equal(f.clock.Now()) {
		t.Fatalf("result times = %+v", result.Device)
	}
	if strings.Contains(f.logs.String(), result.DeviceToken) {
		t.Fatal("device token reached the log")
	}
}

func TestClaimAfterClaimed(t *testing.T) {
	f := newClaimFixture(t)
	token, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := f.svc.Claim(context.Background(), token, laptop()); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := f.svc.Claim(context.Background(), token, laptop()); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("second claim error = %v, want ErrAlreadyClaimed", err)
	}
	if f.store.deviceCount() != 1 {
		t.Fatalf("devices = %d, want 1", f.store.deviceCount())
	}
}

func TestClaimConcurrentExactlyOneWins(t *testing.T) {
	f := newClaimFixture(t)
	token, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	const claimers = 8
	var wg sync.WaitGroup
	results := make(chan error, claimers)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Claim(context.Background(), token, laptop())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrAlreadyClaimed), errors.Is(err, ErrSetupTokenInvalid):
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if won != 1 || f.store.deviceCount() != 1 {
		t.Fatalf("won=%d devices=%d, want exactly one", won, f.store.deviceCount())
	}
}

func TestClaimRejections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   func(valid string) string
		device  adminv1.DeviceInfo
		advance time.Duration
		want    error
	}{
		{"wrong token", func(string) string { return "bdst1_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }, laptop(), 0, ErrSetupTokenInvalid},
		{"malformed token", func(string) string { return "nonsense" }, laptop(), 0, ErrSetupTokenInvalid},
		{"empty token", func(string) string { return "" }, laptop(), 0, ErrSetupTokenInvalid},
		{"expired token", func(valid string) string { return valid }, laptop(), 24*time.Hour + time.Second, ErrSetupTokenInvalid},
		{"empty device name", func(valid string) string { return valid }, adminv1.DeviceInfo{Name: "  "}, 0, ErrValidationFailed},
		{"device name over 64 characters", func(valid string) string { return valid }, adminv1.DeviceInfo{Name: strings.Repeat("a", 65)}, 0, ErrValidationFailed},
		{"device name with control character", func(valid string) string { return valid }, adminv1.DeviceInfo{Name: "bad\x07name"}, 0, ErrValidationFailed},
		{"client version over 32 characters", func(valid string) string { return valid }, adminv1.DeviceInfo{Name: "ok", ClientVersion: strings.Repeat("1", 33)}, 0, ErrValidationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaimFixture(t)
			valid, _, err := f.svc.MintSetupTokenIfUnclaimed(context.Background())
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			f.clock.Advance(tc.advance)
			_, err = f.svc.Claim(context.Background(), tc.token(valid), tc.device)
			if !errors.Is(err, tc.want) {
				t.Fatalf("claim error = %v, want %v", err, tc.want)
			}
			if f.store.deviceCount() != 0 || !f.store.tokenStored() {
				t.Fatalf("rejected claim changed state: devices=%d token=%v", f.store.deviceCount(), f.store.tokenStored())
			}
		})
	}
}

func TestValidationErrorCarriesField(t *testing.T) {
	_, err := validateDeviceInfo(adminv1.DeviceInfo{Name: ""})
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Field != "device_name" || verr.Reason == "" {
		t.Fatalf("error = %v, want a device_name ValidationError", err)
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	valid := Dependencies{
		Relay:    stubRelay{},
		Claims:   &memClaimStore{},
		Tokens:   &countingIssuer{},
		Clock:    clock.NewFake(time.Unix(0, 0)),
		Random:   &sequenceReader{},
		Logger:   slog.New(slog.NewJSONHandler(&strings.Builder{}, nil)),
		ServerID: testServerID,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"relay", func(d *Dependencies) { d.Relay = nil }},
		{"claims", func(d *Dependencies) { d.Claims = nil }},
		{"tokens", func(d *Dependencies) { d.Tokens = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"random", func(d *Dependencies) { d.Random = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
		{"server id", func(d *Dependencies) { d.ServerID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := valid
			tc.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := New(valid); err != nil {
		t.Fatalf("New with every dependency: %v", err)
	}
}
