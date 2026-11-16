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

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

const (
	testServerID = "srv1"
	testDeviceID = "dev-self"
)

type fakeClient struct {
	mu sync.Mutex

	info       adminv1.ServerInfo
	infoErr    error
	devices    []adminv1.Device
	devicesErr error
	renameErr  error
	revokeErrs []error
	rotate     adminv1.DeviceCredentials
	rotateErr  error
	telegram   adminv1.TelegramStatus
	statusErr  error
	unlinkErr  error
	auditPages []adminv1.AuditPage
	auditErr   error

	revokeCalls []revokeCall
	renames     []string
	unlinks     int
	audits      []adminv1.AuditQuery
	rotations   int
}

type revokeCall struct {
	id          string
	confirmLast bool
}

func (f *fakeClient) ServerInfo(context.Context) (adminv1.ServerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.infoErr
}

func (f *fakeClient) ListDevices(context.Context) (adminv1.DeviceList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return adminv1.DeviceList{Devices: append([]adminv1.Device(nil), f.devices...)}, f.devicesErr
}

func (f *fakeClient) RenameDevice(_ context.Context, id string, req adminv1.UpdateDeviceRequest) (adminv1.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, id+"="+req.Name)
	if f.renameErr != nil {
		return adminv1.Device{}, f.renameErr
	}
	for _, d := range f.devices {
		if d.DeviceID == id {
			d.Name = req.Name
			return d, nil
		}
	}
	return adminv1.Device{DeviceID: id, Name: req.Name}, nil
}

func (f *fakeClient) RevokeDevice(_ context.Context, id string, confirmLast bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeCalls = append(f.revokeCalls, revokeCall{id, confirmLast})
	if len(f.revokeErrs) == 0 {
		return nil
	}
	err := f.revokeErrs[0]
	f.revokeErrs = f.revokeErrs[1:]
	return err
}

func (f *fakeClient) RotateDeviceToken(context.Context) (adminv1.DeviceCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotations++
	return f.rotate, f.rotateErr
}

func (f *fakeClient) TelegramStatus(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.telegram, f.statusErr
}

func (f *fakeClient) UnlinkTelegram(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlinks++
	if f.unlinkErr != nil {
		return adminv1.TelegramStatus{}, f.unlinkErr
	}
	return adminv1.TelegramStatus{State: adminv1.TelegramReady, Next: adminv1.NextPhone}, nil
}

func (f *fakeClient) Audit(_ context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, q)
	if f.auditErr != nil {
		return adminv1.AuditPage{}, f.auditErr
	}
	if len(f.auditPages) == 0 {
		return adminv1.AuditPage{}, nil
	}
	page := f.auditPages[0]
	f.auditPages = f.auditPages[1:]
	return page, nil
}

type memTokens struct {
	mu      sync.Mutex
	kind    model.TokenStorage
	tokens  map[string]string
	setErr  error
	deletes []string
	order   *[]string
}

func newMemTokens(kind model.TokenStorage) *memTokens {
	return &memTokens{kind: kind, tokens: map[string]string{}}
}

func (m *memTokens) Get(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.tokens[id]
	if !ok {
		return "", model.ErrTokenNotFound
	}
	return tok, nil
}

func (m *memTokens) Set(id, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.order != nil {
		*m.order = append(*m.order, "token.set")
	}
	if m.setErr != nil {
		return m.setErr
	}
	m.tokens[id] = token
	return nil
}

func (m *memTokens) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, id)
	delete(m.tokens, id)
	return nil
}

func (m *memTokens) Kind() model.TokenStorage { return m.kind }

type memProfiles struct {
	mu       sync.Mutex
	profiles map[string]model.Profile
	putErr   error
	order    *[]string
}

func newMemProfiles(p model.Profile) *memProfiles {
	return &memProfiles{profiles: map[string]model.Profile{p.ServerID: p}}
}

func (m *memProfiles) List() ([]model.Profile, error) { return nil, nil }

func (m *memProfiles) Get(id string) (model.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.profiles[id]
	if !ok {
		return model.Profile{}, model.ErrProfileNotFound
	}
	return p, nil
}

func (m *memProfiles) Put(p model.Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.order != nil {
		*m.order = append(*m.order, "profile.put")
	}
	if m.putErr != nil {
		return m.putErr
	}
	m.profiles[p.ServerID] = p
	return nil
}

func (m *memProfiles) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.profiles, id)
	return nil
}

func (m *memProfiles) LastUsed() (string, error) { return "", nil }
func (m *memProfiles) SetLastUsed(string) error  { return nil }

type fakeProber struct {
	info adminv1.CertificateInfo
	err  error
	urls []string
}

func (f *fakeProber) Probe(_ context.Context, baseURL string) (adminv1.CertificateInfo, error) {
	f.urls = append(f.urls, baseURL)
	return f.info, f.err
}

type memPrefs struct {
	strings map[string]string
	ints    map[string]int
}

func newMemPrefs() *memPrefs {
	return &memPrefs{strings: map[string]string{}, ints: map[string]int{}}
}

func (m *memPrefs) StringWithFallback(key, fallback string) string {
	if v, ok := m.strings[key]; ok {
		return v
	}
	return fallback
}

func (m *memPrefs) SetString(key, value string) { m.strings[key] = value }

func (m *memPrefs) IntWithFallback(key string, fallback int) int {
	if v, ok := m.ints[key]; ok {
		return v
	}
	return fallback
}

func (m *memPrefs) SetInt(key string, value int) { m.ints[key] = value }

type harness struct {
	vm       *ViewModel
	client   *fakeClient
	tokens   *memTokens
	profiles *memProfiles
	keychain *memTokens
	prober   *fakeProber
	prefs    *memPrefs
	clientTk []string
	profile  model.Profile
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		client:   &fakeClient{},
		tokens:   newMemTokens(model.TokenFile),
		keychain: newMemTokens(model.TokenKeychain),
		prober:   &fakeProber{},
		prefs:    newMemPrefs(),
		profile: model.Profile{
			ServerID:      testServerID,
			Label:         "home server",
			BaseURL:       "https://203.0.113.10:9443",
			Trust:         adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: "SHA256:" + fingerprintHex()},
			DeviceID:      testDeviceID,
			DeviceName:    "Office laptop",
			TokenStorage:  model.TokenFile,
			TokenIssuedAt: testNow.Add(-48 * time.Hour),
		},
	}
	h.tokens.tokens[testServerID] = "old-token"
	h.profiles = newMemProfiles(h.profile)
	h.client.devices = []adminv1.Device{
		{DeviceID: testDeviceID, Name: "Office laptop", Current: true, CreatedVia: "setup"},
		{DeviceID: "dev-other", Name: "Phone"},
	}
	h.vm = New(h.client, func() time.Time { return testNow }, Session{
		Profile:            h.profile,
		Tokens:             h.tokens,
		Profiles:           h.profiles,
		Keychain:           h.keychain,
		Prober:             h.prober,
		SetClientToken:     func(tok string) { h.clientTk = append(h.clientTk, tok) },
		Preferences:        h.prefs,
		AppVersion:         "0.1.0",
		DefaultDownloadDir: "/home/me/Downloads",
	})
	return h
}

func fingerprintHex() string {
	return "4f1c9a0e00112233445566778899aabbccddeeff00112233445566778899aabb"
}

func apiError(code adminv1.Code) error {
	return &adminv1.APIError{Status: 409, Body: adminv1.ErrorBody{Code: code, RequestID: "req"}}
}

var errBoom = errors.New("boom")
