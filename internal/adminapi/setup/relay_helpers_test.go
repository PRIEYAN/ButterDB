package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

const (
	linkAPIID       = 1234567
	linkAPIHash     = "0123456789abcdef0123456789abcdef"
	linkPhone       = "+447700900134"
	linkCode        = "12345"
	linkPassword    = "correct horse battery"
	linkPasswordTip = "the usual"
	linkUserID      = int64(777000123)
	otherUserID     = int64(777000999)
)

var _ LoginRelay = (*identity.Service)(nil)

type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) Remove(path string) error { return os.Remove(path) }

type memAccounts struct {
	mu   sync.Mutex
	rows map[string]identity.StoredAccount
}

func (m *memAccounts) Get(_ context.Context, role string) (identity.StoredAccount, bool, error) {
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

func (m *memAccounts) Link(_ context.Context, account identity.StoredAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

type sessionCounter struct {
	mu      sync.Mutex
	deletes int
}

func (s *sessionCounter) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	return nil
}

func (s *sessionCounter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes
}

type byteCounter struct{ next byte }

func (b *byteCounter) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = b.next
		b.next++
	}
	return len(p), nil
}

type linkFixture struct {
	relay    *Service
	identity *identity.Service
	client   *faketg.Client
	accounts *memAccounts
	sessions *sessionCounter
	clock    *clock.Fake
	logs     *bytes.Buffer
	dir      string
}

func newLinkFixture(t *testing.T, script faketg.LoginScript, bound *identity.StoredAccount) *linkFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 12, 9, 41, 0, 0, time.UTC))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("faketg network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: linkUserID, FirstName: "Ada", Username: "ada", Login: &script})
	if err != nil {
		t.Fatalf("faketg account: %v", err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	f := &linkFixture{
		client:   client,
		accounts: &memAccounts{rows: map[string]identity.StoredAccount{}},
		sessions: &sessionCounter{},
		clock:    clk,
		logs:     &bytes.Buffer{},
		dir:      t.TempDir(),
	}
	if bound != nil {
		f.accounts.rows[bound.Role] = *bound
	}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	id, err := identity.NewService(identity.Dependencies{
		Role:            identity.RolePrimary,
		CredentialsPath: filepath.Join(f.dir, "secrets", "telegram_app.json"),
		Writer:          writer,
		Reader:          osFiles{},
		Remover:         osFiles{},
		Auth:            client,
		Self:            client,
		Accounts:        f.accounts,
		Sessions:        f.sessions,
		Clock:           clk,
		Random:          &byteCounter{},
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	f.identity = id
	svc, err := New(Dependencies{
		Relay:    id,
		Claims:   &memClaimStore{},
		Tokens:   &countingIssuer{},
		Clock:    clk,
		Random:   &sequenceReader{},
		Logger:   logger,
		ServerID: testServerID,
	})
	if err != nil {
		t.Fatalf("setup service: %v", err)
	}
	f.relay = svc
	return f
}

func plainScript() faketg.LoginScript {
	return faketg.LoginScript{Phone: linkPhone, Code: linkCode, CodeLifetime: 5 * time.Minute}
}

func passwordScript() faketg.LoginScript {
	script := plainScript()
	script.Password = linkPassword
	script.PasswordHint = linkPasswordTip
	return script
}

func (f *linkFixture) start(t *testing.T) identity.Snapshot {
	t.Helper()
	ctx := context.Background()
	if err := f.relay.Relay().SaveCredentials(ctx, linkAPIID, linkAPIHash); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	snap, err := f.relay.Relay().Start(ctx, linkPhone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if snap.State != identity.StateAwaitingCode || snap.Login == nil {
		t.Fatalf("snapshot after start = %+v", snap)
	}
	return snap
}

func (f *linkFixture) toPasswordStage(t *testing.T) identity.Snapshot {
	t.Helper()
	snap := f.start(t)
	next, err := f.relay.Relay().SubmitCode(context.Background(), snap.Login.LoginID, linkCode)
	if err != nil {
		t.Fatalf("submit code: %v", err)
	}
	if next.State != identity.StateAwaitingPassword {
		t.Fatalf("state after code = %s, want awaiting_password", next.State)
	}
	return next
}

func snapshotText(t *testing.T, snap identity.Snapshot, err error) string {
	t.Helper()
	raw, marshalErr := json.Marshal(snap)
	if marshalErr != nil {
		t.Fatalf("marshal snapshot: %v", marshalErr)
	}
	text := string(raw)
	if err != nil {
		text += "\n" + err.Error()
	}
	return text
}
