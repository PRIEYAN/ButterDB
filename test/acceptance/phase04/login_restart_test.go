package phase04

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

const (
	apiHash   = "0123456789abcdef0123456789abcdef"
	phone     = "+447700900134"
	loginCode = "24680"
	password  = "hunter2"
)

type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) Remove(path string) error { return os.Remove(path) }

type recordStore struct{ repo *accountrepo.Repository }

func (s recordStore) Get(ctx context.Context, role string) (identity.StoredAccount, bool, error) {
	rec, found, err := s.repo.Get(ctx, role)
	return identity.StoredAccount(rec), found, err
}

func (s recordStore) FindByUser(ctx context.Context, user int64) (string, bool, error) {
	return s.repo.FindByUser(ctx, user)
}

func (s recordStore) Link(ctx context.Context, account identity.StoredAccount) error {
	return s.repo.Link(ctx, accountrepo.Record(account))
}

func (s recordStore) Unlink(ctx context.Context, role string, at int64) error {
	return s.repo.Unlink(ctx, role, at)
}

type process struct {
	identity *identity.Service
	sessions *identity.SessionStore
	db       *sqlitedb.DB
}

func (p *process) stop(t *testing.T) {
	t.Helper()
	if err := p.db.Close(); err != nil {
		t.Fatalf("close control database: %v", err)
	}
}

type restartRig struct {
	dir     string
	keyPath string
	w       *world
	login   *faketg.Client
}

func newRestartRig(t *testing.T) *restartRig {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	w := newWorld(t)
	login, err := w.network.AddAccount(faketg.AccountConfig{
		UserID: userID + 1, FirstName: "Grace", Username: "grace",
		Login: &faketg.LoginScript{Phone: phone, Code: loginCode, Password: password, PasswordHint: "pet"},
	})
	if err != nil {
		t.Fatalf("login account: %v", err)
	}
	return &restartRig{dir: dir, keyPath: keyPath, w: w, login: login}
}

func (r *restartRig) boot(t *testing.T, keyPath string) *process {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, filepath.Join(r.dir, "control.db"), sqlitedb.Options{}, discardLogger(),
		sqlitedb.Dependencies{Clock: r.w.clk, FS: fsx.OS{}})
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	if _, err := schema.Apply(ctx, db, schema.Control); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo, err := accountrepo.New(db)
	if err != nil {
		t.Fatalf("accountrepo: %v", err)
	}
	ring, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(ring.Close)
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	sessions, err := identity.NewSessionStore(identity.SessionStoreDependencies{
		Role:    identity.RolePrimary,
		Path:    filepath.Join(r.dir, "secrets", "session_primary.json"),
		Keys:    ringKeys{ring: ring},
		Writer:  writer,
		Reader:  osFiles{},
		Remover: osFiles{},
		Random:  cryptorand.Reader,
	})
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	service, err := identity.NewService(identity.Dependencies{
		Role:            identity.RolePrimary,
		CredentialsPath: filepath.Join(r.dir, "secrets", "telegram_app.json"),
		Writer:          writer,
		Reader:          osFiles{},
		Remover:         osFiles{},
		Auth:            r.login,
		Self:            r.login,
		Accounts:        recordStore{repo: repo},
		Sessions:        sessions,
		Clock:           r.w.clk,
		Random:          cryptorand.Reader,
		Logger:          discardLogger(),
	})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return &process{identity: service, sessions: sessions, db: db}
}

type ringKeys struct{ ring *keyring.Ring }

func (k ringKeys) LeaseSessionKey() ([]byte, error) {
	key := k.ring.SessionKey()
	defer key.Clear()
	return bytes.Clone(key.Bytes()), nil
}

func TestLoginStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()

	t.Run("a linked account and its sealed session survive a restart", func(t *testing.T) {
		rig := newRestartRig(t)
		first := rig.boot(t, rig.keyPath)
		if err := first.identity.SaveCredentials(ctx, 12345, apiHash); err != nil {
			t.Fatalf("SaveCredentials: %v", err)
		}
		snap, err := first.identity.Start(ctx, phone)
		if err != nil || snap.State != identity.StateAwaitingCode {
			t.Fatalf("Start = %+v, %v; want awaiting_code", snap, err)
		}
		loginID := snap.Login.LoginID
		snap, err = first.identity.SubmitCode(ctx, loginID, loginCode)
		if err != nil || snap.State != identity.StateAwaitingPassword || snap.Login.PasswordHint != "pet" {
			t.Fatalf("SubmitCode = %+v, %v; want awaiting_password with the hint", snap, err)
		}
		snap, err = first.identity.SubmitPassword(ctx, loginID, password)
		if err != nil || snap.State != identity.StateLinked {
			t.Fatalf("SubmitPassword = %+v, %v; want linked", snap, err)
		}
		session := []byte("raw gotd auth key and dc state")
		if err := first.sessions.StoreSession(ctx, session); err != nil {
			t.Fatalf("StoreSession: %v", err)
		}
		first.stop(t)

		second := rig.boot(t, rig.keyPath)
		defer second.stop(t)
		snap, err = second.identity.Status(ctx)
		if err != nil || snap.State != identity.StateLinked || snap.Account == nil || snap.Account.UserID != userID+1 {
			t.Fatalf("status after restart = %+v, %v; want linked as %d", snap, err, userID+1)
		}
		if snap.Credentials.APIID != 12345 || !snap.Credentials.APIHashSet {
			t.Fatalf("credentials after restart = %+v", snap.Credentials)
		}
		loaded, err := second.sessions.LoadSession(ctx)
		if err != nil || !bytes.Equal(loaded, session) {
			t.Fatalf("LoadSession after restart = %q, %v", loaded, err)
		}
		if _, err := second.identity.Start(ctx, phone); !errors.Is(err, identity.ErrAlreadyLinked) {
			t.Fatalf("Start on a linked deployment = %v, want ErrAlreadyLinked", err)
		}
		raw, err := os.ReadFile(filepath.Join(rig.dir, "secrets", "session_primary.json"))
		if err != nil || bytes.Contains(raw, session) {
			t.Fatalf("session file readable = %v, contains plaintext = %v", err, bytes.Contains(raw, session))
		}
	})

	t.Run("another server key cannot open the stored session", func(t *testing.T) {
		rig := newRestartRig(t)
		first := rig.boot(t, rig.keyPath)
		if err := first.sessions.StoreSession(ctx, []byte("session bytes")); err != nil {
			t.Fatalf("StoreSession: %v", err)
		}
		first.stop(t)

		otherKey := filepath.Join(t.TempDir(), "other.key")
		if _, err := keyring.Generate(otherKey, cryptorand.Reader); err != nil {
			t.Fatalf("generate other key: %v", err)
		}
		second := rig.boot(t, otherKey)
		defer second.stop(t)
		if _, err := second.sessions.LoadSession(ctx); !errors.Is(err, identity.ErrSessionUnseal) {
			t.Fatalf("LoadSession with another key = %v, want ErrSessionUnseal", err)
		}
	})

	t.Run("a half-finished login is dropped on restart and can start over", func(t *testing.T) {
		rig := newRestartRig(t)
		first := rig.boot(t, rig.keyPath)
		if err := first.identity.SaveCredentials(ctx, 12345, apiHash); err != nil {
			t.Fatalf("SaveCredentials: %v", err)
		}
		snap, err := first.identity.Start(ctx, phone)
		if err != nil || snap.State != identity.StateAwaitingCode {
			t.Fatalf("Start = %+v, %v", snap, err)
		}
		first.stop(t)

		second := rig.boot(t, rig.keyPath)
		defer second.stop(t)
		snap, err = second.identity.Status(ctx)
		if err != nil || snap.State != identity.StateReady || snap.Login != nil {
			t.Fatalf("status after restart = %+v, %v; want ready with no login in progress", snap, err)
		}
		if _, err := second.identity.SubmitCode(ctx, "lg_stale", loginCode); !errors.Is(err, identity.ErrLoginNotActive) {
			t.Fatalf("stale submit = %v, want ErrLoginNotActive", err)
		}
		snap, err = second.identity.Start(ctx, phone)
		if err != nil || snap.State != identity.StateAwaitingCode {
			t.Fatalf("second Start = %+v, %v", snap, err)
		}
		if snap, err = second.identity.SubmitCode(ctx, snap.Login.LoginID, loginCode); err != nil || snap.State != identity.StateAwaitingPassword {
			t.Fatalf("SubmitCode = %+v, %v", snap, err)
		}
	})

	t.Run("a rejected code keeps the attempt open across steps", func(t *testing.T) {
		rig := newRestartRig(t)
		proc := rig.boot(t, rig.keyPath)
		defer proc.stop(t)
		if err := proc.identity.SaveCredentials(ctx, 12345, apiHash); err != nil {
			t.Fatalf("SaveCredentials: %v", err)
		}
		snap, err := proc.identity.Start(ctx, phone)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		snap, err = proc.identity.SubmitCode(ctx, snap.Login.LoginID, "00000")
		if !errors.Is(err, identity.ErrCodeInvalid) || snap.State != identity.StateAwaitingCode || snap.Login.CodeAttemptsLeft != identity.MaxCodeAttempts-1 {
			t.Fatalf("wrong code = %+v, %v; want a retry in place with one attempt spent", snap, err)
		}
		var step *identity.StepError
		if !errors.As(err, &step) || step.AttemptsLeft != identity.MaxCodeAttempts-1 {
			t.Fatalf("step error = %v", err)
		}
	})
}
