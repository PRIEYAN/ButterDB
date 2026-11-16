package vm

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	f := newFixture(t)
	good := Dependencies{
		Dialer:      f.dialer,
		Tokens:      TokenStores{Keychain: f.keychain, File: f.file, Memory: f.memory},
		Profiles:    f.profiles,
		Now:         func() time.Time { return testNow },
		OnConnected: func(model.Profile) {},
	}
	cases := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"dialer", func(d *Dependencies) { d.Dialer = nil }},
		{"keychain", func(d *Dependencies) { d.Tokens.Keychain = nil }},
		{"file store", func(d *Dependencies) { d.Tokens.File = nil }},
		{"memory store", func(d *Dependencies) { d.Tokens.Memory = nil }},
		{"profiles", func(d *Dependencies) { d.Profiles = nil }},
		{"clock", func(d *Dependencies) { d.Now = nil }},
		{"on connected", func(d *Dependencies) { d.OnConnected = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := good
			tc.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, ErrMissingDependency) || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("New error = %v, want ErrMissingDependency naming %q", err, tc.name)
			}
		})
	}
	if _, err := New(good); err != nil {
		t.Fatalf("New with all dependencies: %v", err)
	}
}

func TestContinueRejectsBadURLInline(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"plain http to a public host", "http://butterdb.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.vm.Continue(context.Background(), tc.input, "")
			p := f.state().Problem
			if p == nil || p.Field != FieldURL || p.Presentation != model.PresentInline {
				t.Fatalf("problem = %+v, want inline problem on url", p)
			}
			if len(f.log.all()) != 0 {
				t.Fatalf("calls made for a bad url: %v", f.log.all())
			}
		})
	}
}

func TestProbeThenConfirmPins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.vm.Continue(ctx, "203.0.113.10", "home")
	s := f.state()
	if s.Step != StepConfirmCertificate || s.Certificate == nil {
		t.Fatalf("after Continue: step %q certificate %+v", s.Step, s.Certificate)
	}
	if f.dialer.probed[0] != testBaseURL {
		t.Fatalf("probed %q, want %q", f.dialer.probed[0], testBaseURL)
	}
	if f.log.indexOf("dialer.Dial") >= 0 || f.client.infoCalls != 0 {
		t.Fatalf("server contacted before the owner confirmed: %v", f.log.all())
	}
	wantLines := "4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e"
	if s.Certificate.Fingerprint != wantLines || s.Certificate.Subject != "CN=butterdbd" {
		t.Fatalf("certificate view = %+v", s.Certificate)
	}
	f.vm.ConfirmCertificate(ctx, false)
	cfg := f.dialer.lastDial()
	wantTrust := adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: testPin}
	if cfg.BaseURL != testBaseURL || !reflect.DeepEqual(cfg.Trust, wantTrust) || cfg.Token != "" {
		t.Fatalf("dial config = %+v, want pinned anonymous client", cfg)
	}
	if got := f.state().Step; got != StepClaim {
		t.Fatalf("step after confirm = %q, want claim", got)
	}
	if f.profiles.count() != 0 {
		t.Fatalf("a profile was saved before claim")
	}
}

func TestProbeSystemTrustedCertificate(t *testing.T) {
	cases := []struct {
		name      string
		pinKey    bool
		wantTrust adminv1.Trust
	}{
		{"trust system certificate", false, adminv1.Trust{Mode: adminv1.TrustSystem}},
		{"advanced pin instead", true, adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: testPin}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.dialer.probe.VerifiesWithSystemRoots = true
			f.vm.Continue(context.Background(), "butterdb.example", "")
			f.vm.ConfirmCertificate(context.Background(), tc.pinKey)
			if got := f.dialer.lastDial().Trust; !reflect.DeepEqual(got, tc.wantTrust) {
				t.Fatalf("trust = %+v, want %+v", got, tc.wantTrust)
			}
		})
	}
}

func TestPlainLoopbackSkipsCertificateSteps(t *testing.T) {
	f := newFixture(t)
	f.vm.Continue(context.Background(), "http://localhost:9443", "")
	if f.log.indexOf("dialer.Probe") >= 0 {
		t.Fatalf("plain http was probed")
	}
	s := f.state()
	if s.Step != StepClaim || !s.PlainHTTP {
		t.Fatalf("step %q plainHTTP %v, want claim with warning", s.Step, s.PlainHTTP)
	}
	cfg := f.dialer.lastDial()
	if cfg.BaseURL != "http://localhost:9443" || cfg.Trust.Mode != adminv1.TrustSystem {
		t.Fatalf("dial config = %+v", cfg)
	}
}

func TestProbeFailureShowsDescribedProblem(t *testing.T) {
	f := newFixture(t)
	f.dialer.probeErr = errBoom
	f.vm.Continue(context.Background(), "203.0.113.10", "")
	s := f.state()
	if s.Problem == nil || s.Problem.Title == "" || strings.Contains(s.Problem.Title, "boom") {
		t.Fatalf("problem = %+v, want a catalogue title", s.Problem)
	}
	if s.Step != StepProfiles || s.Loading {
		t.Fatalf("step %q loading %v", s.Step, s.Loading)
	}
}

func TestServerInfoPinDisagreementAborts(t *testing.T) {
	f := newFixture(t)
	f.client.info.TLS.SPKISHA256 = otherPin
	ctx := context.Background()
	f.vm.Continue(ctx, "203.0.113.10", "")
	f.vm.ConfirmCertificate(ctx, false)
	s := f.state()
	if s.Problem == nil || s.Problem.Title != "The server reports a different certificate than the one presented. A proxy or interceptor is in the path." {
		t.Fatalf("problem = %+v", s.Problem)
	}
	if s.Step != StepProfiles {
		t.Fatalf("step = %q, want profiles", s.Step)
	}
	if f.profiles.count() != 0 || f.keychain.has(testServerID) || len(f.connected) != 0 {
		t.Fatalf("something was saved after a pin disagreement")
	}
}

func TestServerInfoEmptyPinIsAccepted(t *testing.T) {
	f := newFixture(t)
	f.client.info.TLS.SPKISHA256 = ""
	f.toClaimStep()
}

func TestAPIVersionMismatchAborts(t *testing.T) {
	f := newFixture(t)
	f.client.info.APIVersion = "v2"
	ctx := context.Background()
	f.vm.Continue(ctx, "203.0.113.10", "")
	f.vm.ConfirmCertificate(ctx, false)
	s := f.state()
	if s.Problem == nil || s.Problem.Title != "This server speaks admin API v2; update butterDB Admin." {
		t.Fatalf("problem = %+v", s.Problem)
	}
	if s.Step != StepProfiles || f.profiles.count() != 0 {
		t.Fatalf("step %q profiles %d", s.Step, f.profiles.count())
	}
}

func TestClaimStoresTokenBeforeProfile(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.vm.SetSetupToken("  " + testSetupTok + "\n")
	f.vm.Claim(context.Background(), testHostName)
	log := f.log.all()
	setIdx := f.log.indexOf("tokens.keychain.Set")
	putIdx := f.log.indexOf("profiles.Put")
	lastIdx := f.log.indexOf("profiles.SetLastUsed")
	connectedIdx := f.log.indexOf("connected")
	if setIdx < 0 || putIdx < 0 || lastIdx < 0 || connectedIdx < 0 {
		t.Fatalf("missing steps in %v", log)
	}
	if !(setIdx < putIdx && putIdx < lastIdx && lastIdx < connectedIdx) {
		t.Fatalf("order = %v, want token store, profile put, last used, connected", log)
	}
	if got := f.keychain.value(testServerID); got != testToken {
		t.Fatalf("stored token = %q", got)
	}
	req := f.client.claimReq
	if req.SetupToken != testSetupTok || req.DeviceName != testHostName || req.Platform != testPlatform || req.ClientVersion != testVersion {
		t.Fatalf("claim request = %+v", req)
	}
	p := f.connected[0]
	if p.ServerID != testServerID || p.TokenStorage != model.TokenKeychain || p.DeviceID != testDeviceID || p.Label != "203.0.113.10" {
		t.Fatalf("connected profile = %+v", p)
	}
	if p.Trust.Mode != adminv1.TrustPinned || p.Trust.SPKISHA256 != testPin || p.BaseURL != testBaseURL {
		t.Fatalf("profile trust = %+v url %q", p.Trust, p.BaseURL)
	}
	if last, _ := f.profiles.LastUsed(); last != testServerID {
		t.Fatalf("last used = %q", last)
	}
}

func TestClaimNeverStoresProfileWhenTokenStoreFails(t *testing.T) {
	f := newFixture(t)
	f.keychain.setErr = errBoom
	f.toClaimStep()
	f.vm.SetSetupToken(testSetupTok)
	f.vm.Claim(context.Background(), testHostName)
	if f.profiles.count() != 0 || len(f.connected) != 0 {
		t.Fatalf("profile saved though the token store failed")
	}
	if f.state().Problem == nil {
		t.Fatalf("no problem shown")
	}
}

func TestClaimClearsSetupToken(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.vm.SetSetupToken(testSetupTok)
	if f.state().SetupToken != testSetupTok {
		t.Fatalf("setup token not held for the form")
	}
	f.vm.Claim(context.Background(), testHostName)
	if got := f.state().SetupToken; got != "" {
		t.Fatalf("setup token after claim = %q, want cleared", got)
	}
	if len(f.connected) != 1 {
		t.Fatalf("connected %d times", len(f.connected))
	}
}

func TestClaimClearsSetupTokenWhileWaitingForStorageChoice(t *testing.T) {
	f := newFixture(t)
	f.keychain.setErr = model.ErrKeychainUnavailable
	f.toClaimStep()
	f.vm.SetSetupToken(testSetupTok)
	f.vm.Claim(context.Background(), testHostName)
	s := f.state()
	if s.Step != StepStorageChoice || s.SetupToken != "" {
		t.Fatalf("step %q setup token %q", s.Step, s.SetupToken)
	}
	if f.profiles.count() != 0 {
		t.Fatalf("profile saved before the storage choice")
	}
}

func TestClaimValidation(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		device    string
		wantField string
	}{
		{"token without prefix", "abc", testHostName, FieldSetupToken},
		{"empty token", "", testHostName, FieldSetupToken},
		{"empty device name", testSetupTok, "  ", FieldDeviceName},
		{"device name too long", testSetupTok, strings.Repeat("a", 65), FieldDeviceName},
		{"device name with control character", testSetupTok, "bad\x07name", FieldDeviceName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.toClaimStep()
			f.vm.SetSetupToken(tc.token)
			f.vm.Claim(context.Background(), tc.device)
			p := f.state().Problem
			if p == nil || p.Field != tc.wantField || p.Presentation != model.PresentInline {
				t.Fatalf("problem = %+v, want inline on %q", p, tc.wantField)
			}
			if f.log.indexOf("client.Claim") >= 0 {
				t.Fatalf("Claim was called despite invalid input")
			}
		})
	}
}

func TestClaimErrorsKeepFormAndToken(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.client.claimErr = apiError(adminv1.CodeSetupTokenInvalid, nil)
	f.vm.SetSetupToken(testSetupTok)
	f.vm.Claim(context.Background(), testHostName)
	s := f.state()
	if s.Step != StepClaim || s.Problem == nil || s.Problem.Code != adminv1.CodeSetupTokenInvalid || s.Problem.Presentation != model.PresentInline {
		t.Fatalf("step %q problem %+v", s.Step, s.Problem)
	}
	if f.keychain.has(testServerID) || f.profiles.count() != 0 {
		t.Fatalf("state saved after a failed claim")
	}
}

func TestClaimAlreadyClaimedSwitchesToPairing(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.client.claimErr = apiError(adminv1.CodeAlreadyClaimed, nil)
	f.client.info.State = "claimed"
	f.client.info.PairingMethods = []string{"host", "saved_messages"}
	f.vm.SetSetupToken(testSetupTok)
	f.vm.Claim(context.Background(), testHostName)
	s := f.state()
	if s.Step != StepPair || !s.SavedMessagesEnabled {
		t.Fatalf("step %q savedMessages %v", s.Step, s.SavedMessagesEnabled)
	}
}

func TestStorageChoiceFallsBackWhenKeychainUnavailable(t *testing.T) {
	cases := []struct {
		name     string
		kind     model.TokenStorage
		store    func(*fixture) *fakeTokens
		wantKind model.TokenStorage
	}{
		{"memory for this session", model.TokenMemory, func(f *fixture) *fakeTokens { return f.memory }, model.TokenMemory},
		{"protected file", model.TokenFile, func(f *fixture) *fakeTokens { return f.file }, model.TokenFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.keychain.setErr = model.ErrKeychainUnavailable
			f.toClaimStep()
			f.vm.SetSetupToken(testSetupTok)
			f.vm.Claim(context.Background(), testHostName)
			f.vm.ChooseStorage(context.Background(), tc.kind)
			if got := tc.store(f).value(testServerID); got != testToken {
				t.Fatalf("chosen store token = %q", got)
			}
			if f.keychain.has(testServerID) {
				t.Fatalf("token reached the keychain")
			}
			if len(f.connected) != 1 || f.connected[0].TokenStorage != tc.wantKind {
				t.Fatalf("connected = %+v", f.connected)
			}
			setIdx := f.log.indexOf("tokens." + string(tc.kind) + ".Set")
			if setIdx < 0 || setIdx > f.log.indexOf("profiles.Put") {
				t.Fatalf("token not stored before profile: %v", f.log.all())
			}
		})
	}
}

func TestChooseStorageIgnoredOutsideChoiceStep(t *testing.T) {
	f := newFixture(t)
	f.vm.ChooseStorage(context.Background(), model.TokenFile)
	if len(f.log.all()) != 0 {
		t.Fatalf("unexpected calls %v", f.log.all())
	}
}

func TestPairingSavedMessagesDisabledWhenUnlinked(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host")
	s := f.state()
	if s.SavedMessagesEnabled {
		t.Fatalf("saved messages enabled though pairing_methods lacks it")
	}
	f.vm.RequestPairing(context.Background(), testHostName)
	if f.log.indexOf("client.RequestPairing") >= 0 {
		t.Fatalf("RequestPairing was called while disabled")
	}
	p := f.state().Problem
	if p == nil || p.Code != adminv1.CodePairingUnavailable {
		t.Fatalf("problem = %+v, want pairing_unavailable", p)
	}
}

func TestPairingSavedMessagesEnabledWhenLinked(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host", "saved_messages")
	if !f.state().SavedMessagesEnabled {
		t.Fatalf("saved messages not enabled")
	}
	expires := testNow.Add(10 * time.Minute)
	f.client.pairRequested = adminv1.PairingRequested{Delivery: "saved_messages", ExpiresAt: expires}
	f.vm.RequestPairing(context.Background(), testHostName)
	s := f.state()
	if !s.PairingRequested || !s.PairingExpiresAt.Equal(expires) {
		t.Fatalf("pairing requested %v expires %v", s.PairingRequested, s.PairingExpiresAt)
	}
	req := f.client.pairingReq
	if req.Delivery != "saved_messages" || req.DeviceName != testHostName || req.Platform != testPlatform {
		t.Fatalf("pairing request = %+v", req)
	}
}

func TestPairingCompleteStoresTokenAndClearsCode(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host")
	f.vm.SetPairingCode(testCodeInput)
	f.vm.CompletePairing(context.Background(), testHostName)
	if got := f.client.completeReq.Code; got != "ABCDE-FGHJK" {
		t.Fatalf("code sent = %q", got)
	}
	if f.state().PairingCode != "" {
		t.Fatalf("pairing code not cleared")
	}
	setIdx, putIdx := f.log.indexOf("tokens.keychain.Set"), f.log.indexOf("profiles.Put")
	if setIdx < 0 || setIdx > putIdx || len(f.connected) != 1 {
		t.Fatalf("order %v connected %d", f.log.all(), len(f.connected))
	}
}

func TestPairingIncompleteCodeIsRejectedLocally(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host")
	f.vm.SetPairingCode("abc")
	f.vm.CompletePairing(context.Background(), testHostName)
	p := f.state().Problem
	if p == nil || p.Field != FieldPairCode || f.log.indexOf("client.CompletePairing") >= 0 {
		t.Fatalf("problem %+v log %v", p, f.log.all())
	}
}

func TestPairingInvalidKeepsForm(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host", "saved_messages")
	f.client.pairErr = apiError(adminv1.CodePairingCodeInvalid, nil)
	f.vm.SetPairingCode(testCodeInput)
	f.vm.CompletePairing(context.Background(), testHostName)
	s := f.state()
	if s.Step != StepPair {
		t.Fatalf("step = %q, want pair", s.Step)
	}
	if s.PairingCode != "" || s.PairingCodeComplete {
		t.Fatalf("code not cleared: %q", s.PairingCode)
	}
	p := s.Problem
	if p == nil || p.Code != adminv1.CodePairingCodeInvalid || p.Field != FieldPairCode || p.Presentation != model.PresentInline {
		t.Fatalf("problem = %+v", p)
	}
	if !strings.Contains(p.Title+" "+p.Body, "Request a new one if it expired.") {
		t.Fatalf("problem text lacks the retry hint: %+v", p)
	}
	if f.keychain.has(testServerID) || f.profiles.count() != 0 {
		t.Fatalf("state saved after an invalid code")
	}
}

func TestPairingNotClaimedSwitchesToClaim(t *testing.T) {
	f := newFixture(t)
	f.toPairStep("host")
	f.client.pairErr = apiError(adminv1.CodeNotClaimed, nil)
	f.client.info.State = "unclaimed"
	f.vm.SetPairingCode(testCodeInput)
	f.vm.CompletePairing(context.Background(), testHostName)
	if got := f.state().Step; got != StepClaim {
		t.Fatalf("step = %q, want claim", got)
	}
}

func staleProfileFixture(t *testing.T) (*fixture, model.Profile) {
	t.Helper()
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-31*24*time.Hour))
	f.client.rotateCreds = credentials(rotatedToken, testNow)
	return f, p
}

func TestConnectUsesStoredTokenAndPin(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-24*time.Hour))
	f.vm.Connect(context.Background(), p.ServerID)
	cfg := f.dialer.lastDial()
	if cfg.Token != testToken || cfg.Trust.SPKISHA256 != testPin || cfg.BaseURL != testBaseURL {
		t.Fatalf("dial config = %+v", cfg)
	}
	if f.log.indexOf("client.RotateDeviceToken") >= 0 {
		t.Fatalf("fresh token was rotated")
	}
	if len(f.connected) != 1 || !f.connected[0].LastUsedAt.Equal(testNow) {
		t.Fatalf("connected = %+v", f.connected)
	}
}

func TestRotateStoresBeforeSwitching(t *testing.T) {
	f, p := staleProfileFixture(t)
	f.vm.Connect(context.Background(), p.ServerID)
	rotateIdx := f.log.indexOf("client.RotateDeviceToken")
	setIdx := f.log.indexOf("tokens.keychain.Set")
	switchIdx := f.log.indexOf("client.SetToken")
	if rotateIdx < 0 || setIdx < 0 || switchIdx < 0 {
		t.Fatalf("missing steps in %v", f.log.all())
	}
	if !(rotateIdx < setIdx && setIdx < switchIdx) {
		t.Fatalf("order = %v, want rotate, store, switch", f.log.all())
	}
	if got := f.keychain.value(p.ServerID); got != rotatedToken {
		t.Fatalf("stored token = %q, want rotated", got)
	}
	if !reflect.DeepEqual(f.client.tokens, []string{rotatedToken}) {
		t.Fatalf("client tokens = %v", f.client.tokens)
	}
	updated, _ := f.profiles.Get(p.ServerID)
	if !updated.TokenIssuedAt.Equal(testNow) {
		t.Fatalf("token_issued_at = %v, want %v", updated.TokenIssuedAt, testNow)
	}
	if len(f.connected) != 1 || f.state().Notice != "" {
		t.Fatalf("connected %d notice %q", len(f.connected), f.state().Notice)
	}
}

func TestRotateStoreFailureKeepsOldToken(t *testing.T) {
	f, p := staleProfileFixture(t)
	f.keychain.setErr = errBoom
	f.vm.Connect(context.Background(), p.ServerID)
	if len(f.client.tokens) != 0 {
		t.Fatalf("client switched to the new token though it was not stored: %v", f.client.tokens)
	}
	if got := f.keychain.value(p.ServerID); got != testToken {
		t.Fatalf("stored token = %q, want the old token kept", got)
	}
	want := "Could not save the rotated token; this device must re-pair within 10 minutes."
	if got := f.state().Notice; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	updated, _ := f.profiles.Get(p.ServerID)
	if !updated.TokenIssuedAt.Equal(p.TokenIssuedAt) {
		t.Fatalf("token_issued_at changed to %v", updated.TokenIssuedAt)
	}
	if len(f.connected) != 1 {
		t.Fatalf("connect should continue with the old token")
	}
}

func TestRotateNotNeededAtThirtyDays(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-30*24*time.Hour))
	f.vm.Connect(context.Background(), p.ServerID)
	if f.log.indexOf("client.RotateDeviceToken") >= 0 {
		t.Fatalf("rotated a token that is exactly 30 days old")
	}
}

func TestRotateFailureOtherThanAuthKeepsConnecting(t *testing.T) {
	f, p := staleProfileFixture(t)
	f.client.rotateErr = errBoom
	f.vm.Connect(context.Background(), p.ServerID)
	if len(f.connected) != 1 || f.state().Notice == "" {
		t.Fatalf("connected %d notice %q", len(f.connected), f.state().Notice)
	}
	if got := f.keychain.value(p.ServerID); got != testToken {
		t.Fatalf("token replaced after failed rotation")
	}
}

func TestRotationUsesStoreOfProfileKind(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenFile, testNow.Add(-40*24*time.Hour))
	f.client.rotateCreds = credentials(rotatedToken, testNow)
	f.vm.Connect(context.Background(), p.ServerID)
	if got := f.file.value(p.ServerID); got != rotatedToken {
		t.Fatalf("file store token = %q", got)
	}
	if f.log.indexOf("tokens.keychain.Set") >= 0 {
		t.Fatalf("keychain written for a file profile")
	}
}

func TestUnauthenticatedDeletesToken(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantNotice string
	}{
		{"unauthenticated", apiError(adminv1.CodeUnauthenticated, nil), "This device is no longer signed in to home server."},
		{"device revoked with time", apiError(adminv1.CodeDeviceRevoked, map[string]any{"revoked_at": "2026-11-15T08:00:00Z"}), "This device was revoked at 2026-11-15T08:00:00Z."},
		{"device revoked without time", apiError(adminv1.CodeDeviceRevoked, nil), "This device was revoked."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.seedProfile(model.TokenKeychain, testNow.Add(-time.Hour))
			f.client.info.State = "claimed"
			f.client.info.PairingMethods = []string{"host"}
			handled := f.vm.HandleSessionError(context.Background(), p.ServerID, tc.err)
			if !handled {
				t.Fatalf("session error not handled")
			}
			if f.keychain.has(p.ServerID) {
				t.Fatalf("stored token was not deleted")
			}
			s := f.state()
			if s.Step != StepPair || s.Notice != tc.wantNotice {
				t.Fatalf("step %q notice %q", s.Step, s.Notice)
			}
			if _, err := f.profiles.Get(p.ServerID); err != nil {
				t.Fatalf("profile removed: %v", err)
			}
		})
	}
}

func TestUnauthenticatedDuringConnectDeletesToken(t *testing.T) {
	f, p := staleProfileFixture(t)
	f.client.rotateErr = apiError(adminv1.CodeUnauthenticated, nil)
	f.client.info.State = "claimed"
	f.vm.Connect(context.Background(), p.ServerID)
	if f.keychain.has(p.ServerID) || len(f.connected) != 0 {
		t.Fatalf("token kept or connected after unauthenticated")
	}
	if got := f.state().Step; got != StepPair {
		t.Fatalf("step = %q, want pair", got)
	}
}

func TestOtherSessionErrorsAreNotHandled(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	for _, err := range []error{errBoom, apiError(adminv1.CodeBucketNotFound, nil), context.Canceled} {
		if f.vm.HandleSessionError(context.Background(), p.ServerID, err) {
			t.Fatalf("error %v was handled", err)
		}
	}
	if !f.keychain.has(p.ServerID) {
		t.Fatalf("token deleted for an unrelated error")
	}
	if f.vm.HandleSessionError(context.Background(), "unknown-server", apiError(adminv1.CodeUnauthenticated, nil)) {
		t.Fatalf("unknown server handled")
	}
}

func TestPinMismatchBlocks(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-time.Hour))
	f.client.infoErr = &adminv1.PinMismatchError{Expected: testPin, Got: otherPin}
	f.vm.Connect(context.Background(), p.ServerID)
	s := f.state()
	if s.Step != StepPinMismatch || s.Mismatch == nil {
		t.Fatalf("step %q mismatch %+v", s.Step, s.Mismatch)
	}
	if s.Mismatch.ServerLabel != "home server" || !strings.HasPrefix(s.Mismatch.Expected, "4f1c 9a0e") || !strings.HasPrefix(s.Mismatch.Received, "aaaa aaaa") {
		t.Fatalf("mismatch view = %+v", s.Mismatch)
	}
	if len(f.connected) != 0 {
		t.Fatalf("connected despite a pin mismatch")
	}
	if !f.keychain.has(p.ServerID) {
		t.Fatalf("token deleted on a pin mismatch")
	}
	stored, _ := f.profiles.Get(p.ServerID)
	if stored.Trust.SPKISHA256 != testPin {
		t.Fatalf("pin changed silently to %q", stored.Trust.SPKISHA256)
	}
}

func TestPinMismatchFromSessionBlocks(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	err := errorsJoin(&adminv1.PinMismatchError{Expected: testPin, Got: otherPin})
	if !f.vm.HandleSessionError(context.Background(), p.ServerID, err) {
		t.Fatalf("pin mismatch not handled")
	}
	if got := f.state().Step; got != StepPinMismatch {
		t.Fatalf("step = %q", got)
	}
}

func errorsJoin(err error) error {
	return errors.Join(errBoom, err)
}

func TestTrustNewKeyRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-time.Hour))
	f.client.infoErr = &adminv1.PinMismatchError{Expected: testPin, Got: otherPin}
	ctx := context.Background()
	f.vm.Connect(ctx, p.ServerID)
	f.client.infoErr = nil
	f.client.info = adminv1.ServerInfo{ServerID: testServerID, APIVersion: "v1", State: "claimed", TLS: adminv1.TLSInfo{SPKISHA256: otherPin}}
	f.dialer.probe.SPKISHA256 = otherPin
	f.dialer.probe.DisplayFingerprint = strings.Repeat("aaaa ", 15) + "aaaa"
	f.log.mu.Lock()
	f.log.entries = nil
	f.log.mu.Unlock()

	f.vm.TrustNewKey(ctx)
	s := f.state()
	if s.Step != StepConfirmCertificate || !s.Rebinding || s.Certificate == nil {
		t.Fatalf("step %q rebinding %v cert %+v", s.Step, s.Rebinding, s.Certificate)
	}
	stored, _ := f.profiles.Get(p.ServerID)
	if stored.Trust.SPKISHA256 != testPin {
		t.Fatalf("pin changed before confirmation: %q", stored.Trust.SPKISHA256)
	}
	if f.log.indexOf("profiles.Put") >= 0 || len(f.connected) != 0 {
		t.Fatalf("profile written or connected before confirmation: %v", f.log.all())
	}

	f.vm.ConfirmCertificate(ctx, false)
	stored, _ = f.profiles.Get(p.ServerID)
	if stored.Trust.SPKISHA256 != otherPin || stored.Trust.Mode != adminv1.TrustPinned {
		t.Fatalf("trust after confirmation = %+v", stored.Trust)
	}
	if !f.keychain.has(p.ServerID) {
		t.Fatalf("stored token was not kept")
	}
	if len(f.connected) != 1 || f.connected[0].DeviceID != testDeviceID {
		t.Fatalf("connected = %+v", f.connected)
	}
}

func TestTrustNewKeyAbortsForDifferentServer(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	f.client.infoErr = &adminv1.PinMismatchError{Expected: testPin, Got: otherPin}
	ctx := context.Background()
	f.vm.Connect(ctx, p.ServerID)
	f.client.infoErr = nil
	f.client.info = adminv1.ServerInfo{ServerID: "someone-else", APIVersion: "v1", State: "claimed"}
	f.dialer.probe.SPKISHA256 = otherPin
	f.vm.TrustNewKey(ctx)
	f.vm.ConfirmCertificate(ctx, false)
	stored, _ := f.profiles.Get(p.ServerID)
	if stored.Trust.SPKISHA256 != testPin || len(f.connected) != 0 {
		t.Fatalf("profile re-pinned for another server")
	}
	if s := f.state(); s.Problem == nil || s.Step != StepProfiles {
		t.Fatalf("step %q problem %+v", s.Step, s.Problem)
	}
}

func TestTrustNewKeyIgnoredWithoutMismatch(t *testing.T) {
	f := newFixture(t)
	f.vm.TrustNewKey(context.Background())
	if len(f.log.all()) != 0 {
		t.Fatalf("unexpected calls %v", f.log.all())
	}
}

func TestDisconnectLeavesMismatchView(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	f.client.infoErr = &adminv1.PinMismatchError{Expected: testPin, Got: otherPin}
	f.vm.Connect(context.Background(), p.ServerID)
	f.vm.Disconnect()
	s := f.state()
	if s.Step != StepProfiles || s.Mismatch != nil || len(s.Profiles) != 1 {
		t.Fatalf("step %q mismatch %+v profiles %d", s.Step, s.Mismatch, len(s.Profiles))
	}
}

func TestConnectWithoutStoredTokenOpensPairing(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenMemory, testNow.Add(-time.Hour))
	f.memory.Delete(p.ServerID)
	f.client.info.State = "claimed"
	f.client.info.PairingMethods = []string{"host", "saved_messages"}
	f.vm.Connect(context.Background(), p.ServerID)
	s := f.state()
	if s.Step != StepPair || !s.SavedMessagesEnabled || s.Notice == "" {
		t.Fatalf("step %q saved %v notice %q", s.Step, s.SavedMessagesEnabled, s.Notice)
	}
	f.vm.SetPairingCode(testCodeInput)
	f.vm.CompletePairing(context.Background(), testHostName)
	if len(f.connected) != 1 || f.connected[0].AddedAt != p.AddedAt || f.connected[0].Label != p.Label {
		t.Fatalf("re-pair did not keep the profile identity: %+v", f.connected)
	}
}

func TestConnectErrors(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*fixture)
		wantIn string
	}{
		{"unreachable server", func(f *fixture) { f.client.infoErr = &net.OpError{Op: "dial", Err: errBoom} }, "Cannot reach"},
		{"api version", func(f *fixture) { f.client.info.APIVersion = "v9" }, "admin API v9"},
		{"different server id", func(f *fixture) { f.client.info.ServerID = "other" }, "different server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.seedProfile(model.TokenKeychain, testNow)
			tc.setup(f)
			f.vm.Connect(context.Background(), p.ServerID)
			s := f.state()
			if s.Problem == nil || !strings.Contains(s.Problem.Title, tc.wantIn) {
				t.Fatalf("problem = %+v, want %q", s.Problem, tc.wantIn)
			}
			if len(f.connected) != 0 || s.Loading {
				t.Fatalf("connected %d loading %v", len(f.connected), s.Loading)
			}
		})
	}
}

func TestAutoConnectOpensLastUsedWithToken(t *testing.T) {
	f := newFixture(t)
	f.seedProfile(model.TokenKeychain, testNow.Add(-time.Hour))
	f.vm.AutoConnect(context.Background())
	if len(f.connected) != 1 {
		t.Fatalf("connected %d times", len(f.connected))
	}
}

func TestAutoConnectSkipsProfileWithoutToken(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	f.keychain.Delete(p.ServerID)
	f.vm.AutoConnect(context.Background())
	s := f.state()
	if len(f.connected) != 0 || len(s.Profiles) != 1 || s.Step != StepProfiles {
		t.Fatalf("connected %d profiles %d step %q", len(f.connected), len(s.Profiles), s.Step)
	}
}

func TestNewURLForKnownServerKeepsToken(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow.Add(-time.Hour))
	f.client.info.State = "claimed"
	ctx := context.Background()
	f.vm.Continue(ctx, "198.51.100.4", "")
	f.dialer.probe.SPKISHA256 = testPin
	f.vm.ConfirmCertificate(ctx, false)
	updated, _ := f.profiles.Get(p.ServerID)
	if updated.BaseURL != "https://198.51.100.4:9443" || updated.Label != p.Label {
		t.Fatalf("profile = %+v", updated)
	}
	if len(f.connected) != 1 || !f.keychain.has(p.ServerID) {
		t.Fatalf("connected %d token kept %v", len(f.connected), f.keychain.has(p.ServerID))
	}
}

func TestRefreshSortsByLastUsed(t *testing.T) {
	f := newFixture(t)
	older := model.Profile{ServerID: "a", Label: "older", LastUsedAt: testNow.Add(-48 * time.Hour), Trust: adminv1.Trust{Mode: adminv1.TrustSystem}}
	newer := model.Profile{ServerID: "b", Label: "newer", LastUsedAt: testNow.Add(-time.Hour), Trust: adminv1.Trust{Mode: adminv1.TrustPinned}}
	f.profiles.Put(older)
	f.profiles.Put(newer)
	f.profiles.SetLastUsed("a")
	f.vm.Refresh(context.Background())
	rows := f.state().Profiles
	if len(rows) != 2 || rows[0].ServerID != "b" || rows[1].ServerID != "a" || !rows[1].Active || rows[0].Active {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestForgetDeletesTokenAndProfile(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenFile, testNow)
	f.vm.Forget(p.ServerID)
	if f.file.has(p.ServerID) || f.profiles.count() != 0 {
		t.Fatalf("token or profile survived Forget")
	}
	if len(f.state().Profiles) != 0 {
		t.Fatalf("profile rows not refreshed")
	}
	f.vm.Forget(p.ServerID)
	if f.state().Problem != nil {
		t.Fatalf("forgetting twice shows a problem: %+v", f.state().Problem)
	}
}

func TestRenameUpdatesLabel(t *testing.T) {
	f := newFixture(t)
	p := f.seedProfile(model.TokenKeychain, testNow)
	f.vm.Rename(p.ServerID, "  cabin  ")
	if got, _ := f.profiles.Get(p.ServerID); got.Label != "cabin" {
		t.Fatalf("label = %q", got.Label)
	}
	f.vm.Rename(p.ServerID, "   ")
	if got, _ := f.profiles.Get(p.ServerID); got.Label != "cabin" {
		t.Fatalf("blank rename changed the label to %q", got.Label)
	}
}

func TestCancelClearsSecretsAndFlow(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.vm.SetSetupToken(testSetupTok)
	f.vm.SetPairingCode(testCodeInput)
	f.vm.Cancel()
	s := f.state()
	if s.Step != StepProfiles || s.SetupToken != "" || s.PairingCode != "" {
		t.Fatalf("state after cancel = %+v", s)
	}
	f.vm.Claim(context.Background(), testHostName)
	if f.log.indexOf("client.Claim") >= 0 {
		t.Fatalf("Claim ran after Cancel")
	}
}

func TestNoSecretsInProblemsOrProfile(t *testing.T) {
	f := newFixture(t)
	f.toClaimStep()
	f.client.claimErr = apiError(adminv1.CodeSetupTokenInvalid, nil)
	f.vm.SetSetupToken(testSetupTok)
	f.vm.Claim(context.Background(), testHostName)
	p := f.state().Problem
	text := p.Title + p.Body + p.Hint
	for key, value := range p.Details {
		text += key + toString(value)
	}
	if strings.Contains(text, testSetupTok) {
		t.Fatalf("problem text carries the setup token")
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
