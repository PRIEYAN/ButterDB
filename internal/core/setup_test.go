package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

const (
	fakeUserID = int64(777000123)
	fakePhone  = "+447700900134"
	fakeCode   = "24680"
)

type scriptedTerminal struct {
	answers map[string][]string
	secrets map[string][]string
	said    []string
}

func (s *scriptedTerminal) next(from map[string][]string, label string) (string, error) {
	queue := from[label]
	if len(queue) == 0 {
		return "", io.EOF
	}
	from[label] = queue[1:]
	return queue[0], nil
}

func (s *scriptedTerminal) Prompt(label string) (string, error) { return s.next(s.answers, label) }

func (s *scriptedTerminal) PromptSecret(label string) (string, error) {
	return s.next(s.secrets, label)
}

func (s *scriptedTerminal) Say(text string) { s.said = append(s.said, text) }

type discardSession struct{}

func (discardSession) Delete(context.Context) error { return nil }

func newFakeFlow(t *testing.T, dir string, login *faketg.LoginScript) *identity.Service {
	t.Helper()
	clk := clock.NewFake(time.Unix(1763000000, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	account, err := network.AddAccount(faketg.AccountConfig{UserID: fakeUserID, FirstName: "Ada", Username: "ada", Login: login})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	db, err := sqlitedb.OpenWith(context.Background(), sqlitedb.Control, filepath.Join(dir, "control.db"), sqlitedb.Options{},
		newDiscardLogger(), sqlitedb.Dependencies{Clock: clk, FS: fsx.OS{}})
	if err != nil {
		t.Fatalf("open control: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Control); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	accounts, err := accountrepo.New(db)
	if err != nil {
		t.Fatalf("accountrepo: %v", err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	service, err := identity.NewService(identity.Dependencies{
		Role:            identity.RolePrimary,
		CredentialsPath: filepath.Join(dir, "secrets", "telegram_app.json"),
		Writer:          writer,
		Reader:          osFileReader{},
		Remover:         fsRemover{fsys: fsx.OS{}},
		Auth:            account,
		Self:            account,
		Accounts:        accountStore{records: accounts},
		Sessions:        discardSession{},
		Clock:           clk,
		Random:          cryptorand.Reader,
		Logger:          newDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return service
}

func runSetup(t *testing.T, flow LoginFlow, terminal Terminal) (identity.Snapshot, error) {
	t.Helper()
	setup, err := NewSetup(SetupDependencies{Flow: flow, Terminal: terminal})
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	return setup.Run(context.Background())
}

func TestNewSetupRejectsMissingDependencies(t *testing.T) {
	cases := []struct {
		name string
		deps SetupDependencies
	}{
		{"nil flow", SetupDependencies{Terminal: &scriptedTerminal{}}},
		{"nil terminal", SetupDependencies{Flow: newFakeFlow(t, t.TempDir(), nil)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := NewSetup(tc.deps); got != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewSetup = %v, %v; want ErrMissingDependency", got, err)
			}
		})
	}
}

func TestSetupLinksAccountWithRetriedCodeAndPassword(t *testing.T) {
	login := &faketg.LoginScript{Phone: fakePhone, Code: fakeCode, Password: "hunter2", PasswordHint: "pet"}
	flow := newFakeFlow(t, t.TempDir(), login)
	terminal := &scriptedTerminal{
		answers: map[string][]string{
			"api_id":                         {"12345"},
			"phone number with country code": {"not a phone", fakePhone},
			"login code":                     {"00000", fakeCode},
		},
		secrets: map[string][]string{
			"api_hash":          {testAPIHash},
			"two-step password": {"wrong", "hunter2"},
		},
	}
	snap, err := runSetup(t, flow, terminal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if snap.State != identity.StateLinked || snap.Account == nil || snap.Account.UserID != fakeUserID {
		t.Fatalf("snapshot = %+v, want linked as %d", snap, fakeUserID)
	}
	said := strings.Join(terminal.said, "\n")
	for _, want := range []string{"not valid", "attempt(s) left", "hint: pet", "Linked to Telegram user"} {
		if !strings.Contains(said, want) {
			t.Errorf("terminal output lacks %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, testAPIHash) || strings.Contains(said, "hunter2") {
		t.Fatalf("terminal output leaked a secret:\n%s", said)
	}
}

func TestSetupReportsAlreadyLinked(t *testing.T) {
	login := &faketg.LoginScript{Phone: fakePhone, Code: fakeCode}
	flow := newFakeFlow(t, t.TempDir(), login)
	first := &scriptedTerminal{
		answers: map[string][]string{"api_id": {"12345"}, "phone number with country code": {fakePhone}, "login code": {fakeCode}},
		secrets: map[string][]string{"api_hash": {testAPIHash}},
	}
	if _, err := runSetup(t, flow, first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second := &scriptedTerminal{}
	snap, err := runSetup(t, flow, second)
	if err != nil || snap.State != identity.StateLinked {
		t.Fatalf("second run = %+v, %v; want linked without prompts", snap, err)
	}
}

func TestSetupStopsWhenTerminalCloses(t *testing.T) {
	flow := newFakeFlow(t, t.TempDir(), &faketg.LoginScript{Phone: fakePhone, Code: fakeCode})
	if _, err := runSetup(t, flow, &scriptedTerminal{}); !errors.Is(err, io.EOF) {
		t.Fatalf("Run = %v, want io.EOF", err)
	}
}

func TestSetupFailsOnExhaustedCodeAttempts(t *testing.T) {
	flow := newFakeFlow(t, t.TempDir(), &faketg.LoginScript{Phone: fakePhone, Code: fakeCode})
	wrong := make([]string, identity.MaxCodeAttempts+1)
	for i := range wrong {
		wrong[i] = "11111"
	}
	terminal := &scriptedTerminal{
		answers: map[string][]string{"api_id": {"12345"}, "phone number with country code": {fakePhone}, "login code": wrong},
		secrets: map[string][]string{"api_hash": {testAPIHash}},
	}
	if _, err := runSetup(t, flow, terminal); !errors.Is(err, identity.ErrAttemptsExhausted) {
		t.Fatalf("Run = %v, want ErrAttemptsExhausted", err)
	}
}
