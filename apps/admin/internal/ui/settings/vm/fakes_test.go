package vm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClient struct {
	mu sync.Mutex

	status      adminv1.Status
	statusErr   error
	telegram    adminv1.TelegramStatus
	telegramErr error
	auditPages  []adminv1.AuditPage
	auditErr    error

	audits []adminv1.AuditQuery
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeClient) TelegramStatus(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.telegram, f.telegramErr
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

type fakeAccount struct {
	project embedded.Project
	open    bool
}

func (a *fakeAccount) Current() (embedded.Project, bool) { return a.project, a.open }

func (a *fakeAccount) Rename(context.Context, embedded.ProjectID, string) error { return nil }

func (a *fakeAccount) ChangePassword(context.Context, embedded.ProjectID, []byte, []byte) error {
	return nil
}

func (a *fakeAccount) ResetKey(context.Context, embedded.ProjectID, []byte, string) (embedded.ResetReport, error) {
	return embedded.ResetReport{}, nil
}

func (a *fakeAccount) Logout(context.Context) error { return nil }

func (a *fakeAccount) RemoveDevice(context.Context) error { return nil }

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
	vm      *ViewModel
	client  *fakeClient
	account *fakeAccount
	prefs   *memPrefs
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		client:  &fakeClient{},
		account: &fakeAccount{project: embedded.Project{ID: "p1", Name: "photos", State: embedded.ProjectReady}, open: true},
		prefs:   newMemPrefs(),
	}
	h.vm = New(h.client, func() time.Time { return testNow }, Session{
		Account:            h.account,
		Preferences:        h.prefs,
		AppVersion:         "0.1.0",
		DefaultDownloadDir: "/home/me/Downloads",
	})
	return h
}

func apiError(code adminv1.Code) error {
	return &adminv1.APIError{Status: 409, Body: adminv1.ErrorBody{Code: code, RequestID: "req"}}
}

var errBoom = errors.New("boom")
