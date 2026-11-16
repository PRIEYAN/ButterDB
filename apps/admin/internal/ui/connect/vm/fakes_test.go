package vm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	testServerID  = "k3v9m2q8t4w7x1z5a6b2c3d4e5"
	testBaseURL   = "https://203.0.113.10:9443"
	testPin       = "SHA256:4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e"
	otherPin      = "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testToken     = "bdt1_a1b2c3d4e5f6g7h2_secretsecretsecretsecretsecretsecretsecretsecret"
	rotatedToken  = "bdt1_a1b2c3d4e5f6g7h2_rotatedrotatedrotatedrotatedrotatedrotatedrotated"
	testSetupTok  = "bdst1_abcdefghijklmnopqrstuvwxyz234567"
	testDeviceID  = "a1b2c3d4e5f6g7h2"
	testPlatform  = "linux/amd64"
	testVersion   = "0.1.0"
	testHostName  = "Office laptop"
	testCodeInput = "abcde-fghjk"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type callLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *callLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (l *callLog) indexOf(entry string) int {
	for i, e := range l.all() {
		if e == entry {
			return i
		}
	}
	return -1
}

type fakeTokens struct {
	kind   model.TokenStorage
	log    *callLog
	mu     sync.Mutex
	tokens map[string]string
	setErr error
}

func newFakeTokens(kind model.TokenStorage, log *callLog) *fakeTokens {
	return &fakeTokens{kind: kind, log: log, tokens: map[string]string{}}
}

func (f *fakeTokens) Get(serverID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, ok := f.tokens[serverID]
	if !ok {
		return "", model.ErrTokenNotFound
	}
	return token, nil
}

func (f *fakeTokens) Set(serverID, token string) error {
	f.log.add("tokens." + string(f.kind) + ".Set")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.tokens[serverID] = token
	return nil
}

func (f *fakeTokens) Delete(serverID string) error {
	f.log.add("tokens." + string(f.kind) + ".Delete")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tokens[serverID]; !ok {
		return model.ErrTokenNotFound
	}
	delete(f.tokens, serverID)
	return nil
}

func (f *fakeTokens) Kind() model.TokenStorage { return f.kind }

func (f *fakeTokens) has(serverID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tokens[serverID]
	return ok
}

func (f *fakeTokens) value(serverID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[serverID]
}

type fakeProfiles struct {
	log      *callLog
	mu       sync.Mutex
	profiles []model.Profile
	last     string
	putErr   error
}

func (f *fakeProfiles) List() ([]model.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Profile(nil), f.profiles...), nil
}

func (f *fakeProfiles) Get(serverID string) (model.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.profiles {
		if p.ServerID == serverID {
			return p, nil
		}
	}
	return model.Profile{}, model.ErrProfileNotFound
}

func (f *fakeProfiles) Put(p model.Profile) error {
	f.log.add("profiles.Put")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	for i, existing := range f.profiles {
		if existing.ServerID == p.ServerID {
			f.profiles[i] = p
			return nil
		}
	}
	f.profiles = append(f.profiles, p)
	return nil
}

func (f *fakeProfiles) Delete(serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.profiles {
		if p.ServerID == serverID {
			f.profiles = append(f.profiles[:i], f.profiles[i+1:]...)
			return nil
		}
	}
	return model.ErrProfileNotFound
}

func (f *fakeProfiles) LastUsed() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, nil
}

func (f *fakeProfiles) SetLastUsed(serverID string) error {
	f.log.add("profiles.SetLastUsed")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = serverID
	return nil
}

func (f *fakeProfiles) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.profiles)
}

type fakeClient struct {
	log           *callLog
	info          adminv1.ServerInfo
	infoErr       error
	claimCreds    adminv1.DeviceCredentials
	claimErr      error
	pairRequested adminv1.PairingRequested
	pairReqErr    error
	pairCreds     adminv1.DeviceCredentials
	pairErr       error
	rotateCreds   adminv1.DeviceCredentials
	rotateErr     error

	mu          sync.Mutex
	claimReq    adminv1.ClaimRequest
	pairingReq  adminv1.PairingRequest
	completeReq adminv1.PairingCompleteRequest
	tokens      []string
	infoCalls   int
}

func (f *fakeClient) ServerInfo(context.Context) (adminv1.ServerInfo, error) {
	f.log.add("client.ServerInfo")
	f.mu.Lock()
	f.infoCalls++
	f.mu.Unlock()
	return f.info, f.infoErr
}

func (f *fakeClient) Claim(_ context.Context, req adminv1.ClaimRequest) (adminv1.DeviceCredentials, error) {
	f.log.add("client.Claim")
	f.mu.Lock()
	f.claimReq = req
	f.mu.Unlock()
	return f.claimCreds, f.claimErr
}

func (f *fakeClient) RequestPairing(_ context.Context, req adminv1.PairingRequest) (adminv1.PairingRequested, error) {
	f.log.add("client.RequestPairing")
	f.mu.Lock()
	f.pairingReq = req
	f.mu.Unlock()
	return f.pairRequested, f.pairReqErr
}

func (f *fakeClient) CompletePairing(_ context.Context, req adminv1.PairingCompleteRequest) (adminv1.DeviceCredentials, error) {
	f.log.add("client.CompletePairing")
	f.mu.Lock()
	f.completeReq = req
	f.mu.Unlock()
	return f.pairCreds, f.pairErr
}

func (f *fakeClient) RotateDeviceToken(context.Context) (adminv1.DeviceCredentials, error) {
	f.log.add("client.RotateDeviceToken")
	return f.rotateCreds, f.rotateErr
}

func (f *fakeClient) SetToken(token string) {
	f.log.add("client.SetToken")
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.mu.Unlock()
}

type fakeDialer struct {
	log      *callLog
	probe    adminv1.CertificateInfo
	probeErr error
	client   *fakeClient
	dialErr  error

	mu     sync.Mutex
	probed []string
	dialed []adminv1.ClientConfig
}

func (d *fakeDialer) Probe(_ context.Context, baseURL string) (adminv1.CertificateInfo, error) {
	d.log.add("dialer.Probe")
	d.mu.Lock()
	d.probed = append(d.probed, baseURL)
	d.mu.Unlock()
	return d.probe, d.probeErr
}

func (d *fakeDialer) Dial(cfg adminv1.ClientConfig) (ConnectClient, error) {
	d.log.add("dialer.Dial")
	d.mu.Lock()
	d.dialed = append(d.dialed, cfg)
	d.mu.Unlock()
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	return d.client, nil
}

func (d *fakeDialer) lastDial() adminv1.ClientConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialed[len(d.dialed)-1]
}

type fixture struct {
	t         *testing.T
	log       *callLog
	vm        *ViewModel
	dialer    *fakeDialer
	client    *fakeClient
	keychain  *fakeTokens
	file      *fakeTokens
	memory    *fakeTokens
	profiles  *fakeProfiles
	connected []model.Profile
	now       time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	log := &callLog{}
	f := &fixture{t: t, log: log, now: testNow}
	f.client = &fakeClient{
		log: log,
		info: adminv1.ServerInfo{
			ServerID:       testServerID,
			APIVersion:     "v1",
			State:          "unclaimed",
			PairingMethods: nil,
			TLS:            adminv1.TLSInfo{Mode: "self-signed", SPKISHA256: testPin},
		},
		claimCreds: credentials(testToken, testNow),
		pairCreds:  credentials(testToken, testNow),
	}
	f.dialer = &fakeDialer{
		log:    log,
		client: f.client,
		probe: adminv1.CertificateInfo{
			SPKISHA256:         testPin,
			DisplayFingerprint: "4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e",
			Subject:            "CN=butterdbd",
			DNSNames:           []string{"butterdb.example"},
			IPAddresses:        []string{"203.0.113.10"},
			NotAfter:           testNow.Add(365 * 24 * time.Hour),
		},
	}
	f.keychain = newFakeTokens(model.TokenKeychain, log)
	f.file = newFakeTokens(model.TokenFile, log)
	f.memory = newFakeTokens(model.TokenMemory, log)
	f.profiles = &fakeProfiles{log: log}
	v, err := New(Dependencies{
		Dialer:            f.dialer,
		Tokens:            TokenStores{Keychain: f.keychain, File: f.file, Memory: f.memory},
		Profiles:          f.profiles,
		Now:               func() time.Time { return f.now },
		OnConnected:       func(p model.Profile) { log.add("connected"); f.connected = append(f.connected, p) },
		DefaultDeviceName: testHostName,
		Platform:          testPlatform,
		ClientVersion:     testVersion,
		UserAgent:         "butterdb-admin/test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.vm = v
	return f
}

func credentials(token string, issued time.Time) adminv1.DeviceCredentials {
	return adminv1.DeviceCredentials{
		Device:      adminv1.Device{DeviceID: testDeviceID, Name: testHostName, TokenIssuedAt: issued},
		DeviceToken: token,
		ServerID:    testServerID,
	}
}

func (f *fixture) state() State { return f.vm.State().Get() }

func (f *fixture) toClaimStep() {
	f.t.Helper()
	ctx := context.Background()
	f.vm.Continue(ctx, "203.0.113.10", "")
	f.vm.ConfirmCertificate(ctx, false)
	if got := f.state().Step; got != StepClaim {
		f.t.Fatalf("step = %q, want claim (problem %+v)", got, f.state().Problem)
	}
}

func (f *fixture) toPairStep(methods ...string) {
	f.t.Helper()
	ctx := context.Background()
	f.client.info.State = "claimed"
	f.client.info.PairingMethods = methods
	f.vm.Continue(ctx, "203.0.113.10", "")
	f.vm.ConfirmCertificate(ctx, false)
	if got := f.state().Step; got != StepPair {
		f.t.Fatalf("step = %q, want pair (problem %+v)", got, f.state().Problem)
	}
}

func (f *fixture) seedProfile(kind model.TokenStorage, issued time.Time) model.Profile {
	f.t.Helper()
	p := model.Profile{
		ServerID:      testServerID,
		Label:         "home server",
		BaseURL:       testBaseURL,
		Trust:         adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: testPin},
		DeviceID:      testDeviceID,
		DeviceName:    testHostName,
		TokenStorage:  kind,
		TokenIssuedAt: issued,
		AddedAt:       testNow.Add(-90 * 24 * time.Hour),
		LastUsedAt:    testNow.Add(-24 * time.Hour),
	}
	if err := f.profiles.Put(p); err != nil {
		f.t.Fatalf("seed Put: %v", err)
	}
	if err := f.profiles.SetLastUsed(p.ServerID); err != nil {
		f.t.Fatalf("seed SetLastUsed: %v", err)
	}
	store := map[model.TokenStorage]*fakeTokens{model.TokenKeychain: f.keychain, model.TokenFile: f.file, model.TokenMemory: f.memory}[kind]
	if err := store.Set(p.ServerID, testToken); err != nil {
		f.t.Fatalf("seed token: %v", err)
	}
	f.log.mu.Lock()
	f.log.entries = nil
	f.log.mu.Unlock()
	return p
}

func apiError(code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: 401, Body: adminv1.ErrorBody{Code: code, RequestID: "req-1", Details: details}}
}

var errBoom = errors.New("boom")
