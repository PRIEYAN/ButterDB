package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
)

func testRing(t *testing.T, seed byte) *keyring.Ring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.key")
	if _, err := keyring.Generate(path, bytes.NewReader(bytes.Repeat([]byte{seed}, 64))); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := keyring.LoadWithRand(path, bytes.NewReader(bytes.Repeat([]byte{seed}, 64)))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	t.Cleanup(ring.Close)
	return ring
}

func newSessionStore(t *testing.T, ring *keyring.Ring, role Role, path string) *SessionStore {
	t.Helper()
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	store, err := NewSessionStore(SessionStoreDependencies{
		Role:    role,
		Path:    path,
		Keys:    ring,
		Writer:  writer,
		Reader:  osFiles{},
		Remover: osFiles{},
		Random:  &counterReader{},
	})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	return store
}

func sessionPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets", "session_primary.json")
}

func TestPrivateSessionStorageMissingSession(t *testing.T) {
	store := newSessionStore(t, testRing(t, 1), RolePrimary, sessionPath(t))
	data, err := store.LoadSession(context.Background())
	if !errors.Is(err, ErrSessionNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadSession error = %v, want ErrSessionNotFound wrapping fs.ErrNotExist", err)
	}
	if data != nil {
		t.Fatalf("LoadSession data = %q, want nil", data)
	}
}

func TestPrivateSessionStorageReplacesSession(t *testing.T) {
	store := newSessionStore(t, testRing(t, 1), RolePrimary, sessionPath(t))
	ctx := context.Background()
	if err := store.StoreSession(ctx, []byte("old session")); err != nil {
		t.Fatalf("store old session: %v", err)
	}
	if err := store.StoreSession(ctx, []byte("new session")); err != nil {
		t.Fatalf("store new session: %v", err)
	}
	data, err := store.LoadSession(ctx)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got, want := string(data), "new session"; got != want {
		t.Fatalf("session = %q, want %q", got, want)
	}
}

func TestSessionStoreDoesNotCreateEmptySession(t *testing.T) {
	path := sessionPath(t)
	store := newSessionStore(t, testRing(t, 1), RolePrimary, path)
	if _, err := store.LoadSession(context.Background()); err == nil {
		t.Fatal("load of a missing session succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("session stat error = %v, want not exist", err)
	}
}

func TestSessionFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.FileMode does not represent ACL permissions")
	}
	path := sessionPath(t)
	store := newSessionStore(t, testRing(t, 1), RolePrimary, path)
	if err := store.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen mode: %v", err)
	}
	if err := store.StoreSession(context.Background(), []byte("session two")); err != nil {
		t.Fatalf("store again: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestSessionFileSealed(t *testing.T) {
	authKey := bytes.Repeat([]byte{0xA5, 0x5A, 0x3C}, 86)
	gotdSession := append([]byte(`{"Version":1,"Data":{"AuthKey":"`), authKey...)
	gotdSession = append(gotdSession, []byte(`","DC":2}}`)...)

	path := sessionPath(t)
	store := newSessionStore(t, testRing(t, 1), RolePrimary, path)
	if err := store.StoreSession(context.Background(), gotdSession); err != nil {
		t.Fatalf("store: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	if bytes.Contains(onDisk, authKey) || bytes.Contains(onDisk, []byte("AuthKey")) {
		t.Fatal("session file holds the raw gotd auth key")
	}
	if bytes.Contains(onDisk, []byte(base64.StdEncoding.EncodeToString(authKey[:48]))) {
		t.Fatal("session file holds the base64 auth key")
	}
	var envelope struct {
		V     int    `json:"v"`
		Nonce string `json:"nonce"`
		CT    string `json:"ct"`
	}
	if err := json.Unmarshal(onDisk, &envelope); err != nil || envelope.V != 1 || envelope.Nonce == "" || envelope.CT == "" {
		t.Fatalf("envelope = %+v, %v", envelope, err)
	}
	if nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce); err != nil || len(nonce) != 24 {
		t.Fatalf("nonce length = %d, %v; want 24", len(nonce), err)
	}

	loaded, err := store.LoadSession(context.Background())
	if err != nil || !bytes.Equal(loaded, gotdSession) {
		t.Fatalf("round trip = %v, %v", bytes.Equal(loaded, gotdSession), err)
	}

	other := newSessionStore(t, testRing(t, 2), RolePrimary, path)
	if _, err := other.LoadSession(context.Background()); !errors.Is(err, ErrSessionUnseal) {
		t.Fatalf("open with another server key error = %v, want ErrSessionUnseal", err)
	}
}

func TestSessionBoundToRole(t *testing.T) {
	ring := testRing(t, 1)
	path := sessionPath(t)
	primary := newSessionStore(t, ring, RolePrimary, path)
	if err := primary.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatalf("store: %v", err)
	}
	replica := newSessionStore(t, ring, RoleReplica, path)
	if _, err := replica.LoadSession(context.Background()); !errors.Is(err, ErrSessionUnseal) {
		t.Fatalf("replica opening a primary session error = %v, want ErrSessionUnseal", err)
	}
}

func TestSessionRejectsMalformedFiles(t *testing.T) {
	ring := testRing(t, 1)
	path := sessionPath(t)
	store := newSessionStore(t, ring, RolePrimary, path)
	if err := store.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatalf("store: %v", err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	tests := []struct {
		name    string
		content []byte
	}{
		{"not json", []byte("raw gotd session")},
		{"unknown version", bytes.Replace(valid, []byte(`"v":1`), []byte(`"v":2`), 1)},
		{"short nonce", []byte(`{"v":1,"nonce":"AAAA","ct":"AAAA"}`)},
		{"bad base64", []byte(`{"v":1,"nonce":"!!","ct":"!!"}`)},
		{"flipped ciphertext", bytes.Replace(valid, []byte(`"ct":"`), []byte(`"ct":"AA`), 1)},
		{"empty", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := store.LoadSession(context.Background()); !errors.Is(err, ErrSessionUnseal) {
				t.Fatalf("error = %v, want ErrSessionUnseal", err)
			}
		})
	}
}

func TestSessionDeleteIsIdempotent(t *testing.T) {
	path := sessionPath(t)
	store := newSessionStore(t, testRing(t, 1), RolePrimary, path)
	if err := store.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatalf("store: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Delete(context.Background()); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if _, err := store.LoadSession(context.Background()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("load after delete error = %v", err)
	}
}

func TestSessionStoreRejectsEmptySessionAndClosedRing(t *testing.T) {
	ring := testRing(t, 1)
	store := newSessionStore(t, ring, RolePrimary, sessionPath(t))
	if err := store.StoreSession(context.Background(), nil); err == nil {
		t.Fatal("empty session stored")
	}
	ring.Close()
	if err := store.StoreSession(context.Background(), []byte("s")); !errors.Is(err, ErrSessionKeyMissing) {
		t.Fatalf("store with closed ring error = %v, want ErrSessionKeyMissing", err)
	}
}

func TestSessionStoreRejectsNilDependencies(t *testing.T) {
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("secretfile: %v", err)
	}
	valid := SessionStoreDependencies{
		Role: RolePrimary, Path: "p", Keys: testRing(t, 1), Writer: writer,
		Reader: osFiles{}, Remover: osFiles{}, Random: &counterReader{},
	}
	tests := []struct {
		name   string
		mutate func(*SessionStoreDependencies)
	}{
		{"keys", func(d *SessionStoreDependencies) { d.Keys = nil }},
		{"writer", func(d *SessionStoreDependencies) { d.Writer = nil }},
		{"reader", func(d *SessionStoreDependencies) { d.Reader = nil }},
		{"remover", func(d *SessionStoreDependencies) { d.Remover = nil }},
		{"random", func(d *SessionStoreDependencies) { d.Random = nil }},
		{"path", func(d *SessionStoreDependencies) { d.Path = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := valid
			tt.mutate(&deps)
			if _, err := NewSessionStore(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
	bad := valid
	bad.Role = "x"
	if _, err := NewSessionStore(bad); err == nil {
		t.Fatal("invalid role accepted")
	}
}

var _ interface {
	LoadSession(ctx context.Context) ([]byte, error)
	StoreSession(ctx context.Context, data []byte) error
} = (*SessionStore)(nil)
