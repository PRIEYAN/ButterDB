package identity

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	testAPIHash   = "0123456789abcdef0123456789abcdef"
	testPhone     = "+447700900134"
	testCodeHash  = "hash-of-the-sent-code"
	testUserID    = int64(777000123)
	otherUserID   = int64(777000999)
	testLoginCode = "98765"
)

type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) Remove(path string) error { return os.Remove(path) }

type scriptedAuth struct {
	mu          sync.Mutex
	sendCode    func(ctx context.Context, phone string) (tgclient.SentCode, error)
	signIn      func(ctx context.Context, phone, hash, code string) (tgclient.Account, error)
	checkPass   func(ctx context.Context, password string) (tgclient.Account, error)
	hint        func(ctx context.Context) (string, error)
	logOut      func(ctx context.Context) error
	sendCalls   int
	signInHash  []string
	signInCodes []string
	logOutCalls int
}

func (a *scriptedAuth) SendCode(ctx context.Context, phone string) (tgclient.SentCode, error) {
	a.mu.Lock()
	a.sendCalls++
	fn := a.sendCode
	a.mu.Unlock()
	if fn == nil {
		return tgclient.SentCode{PhoneCodeHash: testCodeHash, Timeout: 60 * time.Second}, nil
	}
	return fn(ctx, phone)
}

func (a *scriptedAuth) SignIn(ctx context.Context, phone, hash, code string) (tgclient.Account, error) {
	a.mu.Lock()
	a.signInHash = append(a.signInHash, hash)
	a.signInCodes = append(a.signInCodes, code)
	fn := a.signIn
	a.mu.Unlock()
	if fn == nil {
		return tgclient.Account{UserID: testUserID, FirstName: "Ada", Username: "ada"}, nil
	}
	return fn(ctx, phone, hash, code)
}

func (a *scriptedAuth) CheckPassword(ctx context.Context, password string) (tgclient.Account, error) {
	if a.checkPass == nil {
		return tgclient.Account{UserID: testUserID, FirstName: "Ada", Username: "ada"}, nil
	}
	return a.checkPass(ctx, password)
}

func (a *scriptedAuth) GetPasswordHint(ctx context.Context) (string, error) {
	if a.hint == nil {
		return "", nil
	}
	return a.hint(ctx)
}

func (a *scriptedAuth) LogOut(ctx context.Context) error {
	a.mu.Lock()
	a.logOutCalls++
	fn := a.logOut
	a.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

type scriptedSelf struct {
	account tgclient.Account
	err     error
}

func (s scriptedSelf) Self(context.Context) (tgclient.Account, error) { return s.account, s.err }

type memAccounts struct {
	mu   sync.Mutex
	rows map[string]StoredAccount
}

func newMemAccounts() *memAccounts { return &memAccounts{rows: map[string]StoredAccount{}} }

func (m *memAccounts) Get(_ context.Context, role string) (StoredAccount, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[role]
	return row, ok, nil
}

func (m *memAccounts) FindByUser(_ context.Context, userID int64) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for role, row := range m.rows {
		if row.UserID == userID {
			return role, true, nil
		}
	}
	return "", false, nil
}

func (m *memAccounts) Link(_ context.Context, account StoredAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, ok := m.rows[account.Role]; ok {
		account.BoundAt = prior.BoundAt
	}
	m.rows[account.Role] = account
	return nil
}

func (m *memAccounts) Unlink(_ context.Context, role string, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[role]
	row.Linked = false
	row.UnlinkedAt = at
	m.rows[role] = row
	return nil
}

type discardCounter struct {
	mu    sync.Mutex
	calls int
}

func (d *discardCounter) Delete(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return nil
}

type loginFixture struct {
	svc      *Service
	auth     *scriptedAuth
	accounts *memAccounts
	sessions *discardCounter
	clock    *clock.Fake
	dir      string
	self     scriptedSelf
}

type counterReader struct{ next byte }

func (c *counterReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next++
	}
	return len(p), nil
}

func newFixture(t *testing.T) *loginFixture {
	t.Helper()
	return newFixtureIn(t, t.TempDir(), newMemAccounts(), RolePrimary)
}

func newFixtureIn(t *testing.T, dir string, accounts *memAccounts, role Role) *loginFixture {
	t.Helper()
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	f := &loginFixture{
		auth:     &scriptedAuth{},
		accounts: accounts,
		sessions: &discardCounter{},
		clock:    clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		dir:      dir,
		self:     scriptedSelf{account: tgclient.Account{UserID: testUserID}},
	}
	svc, err := NewService(Dependencies{
		Role:            role,
		CredentialsPath: filepath.Join(dir, "secrets", "telegram_app_"+string(role)+".json"),
		Writer:          writer,
		Reader:          osFiles{},
		Remover:         osFiles{},
		Auth:            f.auth,
		Self:            f.self,
		Accounts:        accounts,
		Sessions:        f.sessions,
		Clock:           f.clock,
		Random:          &counterReader{},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		StepTimeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	f.svc = svc
	return f
}

func (f *loginFixture) saveCredentials(t *testing.T) {
	t.Helper()
	if err := f.svc.SaveCredentials(context.Background(), 1234567, testAPIHash); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
}

func (f *loginFixture) startLogin(t *testing.T) Snapshot {
	t.Helper()
	f.saveCredentials(t)
	snap, err := f.svc.Start(context.Background(), testPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return snap
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
