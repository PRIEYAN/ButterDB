package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	testServerID = "k3v9m2q8t4w7x1z5a6b2c3d4e5"
	testToken    = "bdt1_a1b2c3d4e5f6g7h2_secretsecretsecretsecretsecretsecretsecretsecret"
)

func TestKeychainRoundTrip(t *testing.T) {
	keyring.MockInit()
	store := NewKeychain()
	if err := store.Set(testServerID, testToken); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(testServerID)
	if err != nil || got != testToken {
		t.Fatalf("Get = %q, %v; want stored token", got, err)
	}
	stored, err := keyring.Get(KeychainService, testServerID)
	if err != nil || stored != testToken {
		t.Fatalf("keyring entry under service %q = %q, %v", KeychainService, stored, err)
	}
	if err := store.Delete(testServerID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Get after delete error = %v, want ErrTokenNotFound", err)
	}
	if store.Kind() != model.TokenKeychain {
		t.Fatalf("Kind = %q", store.Kind())
	}
}

func TestKeychainNotFoundMapped(t *testing.T) {
	keyring.MockInit()
	store := NewKeychain()
	if _, err := store.Get("missing"); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Get error = %v, want ErrTokenNotFound", err)
	}
	if err := store.Delete("missing"); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Delete error = %v, want ErrTokenNotFound", err)
	}
}

func TestKeychainUnavailableMapped(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"unsupported platform", keyring.ErrUnsupportedPlatform},
		{"dbus session bus missing", errors.New("dbus: couldn't determine address of session bus")},
		{"secret service name missing", errors.New("The name org.freedesktop.secrets was not provided by any .service files")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyring.MockInitWithError(tc.err)
			t.Cleanup(keyring.MockInit)
			store := NewKeychain()
			if err := store.Set(testServerID, testToken); !errors.Is(err, model.ErrKeychainUnavailable) {
				t.Fatalf("Set error = %v, want ErrKeychainUnavailable", err)
			}
			if _, err := store.Get(testServerID); !errors.Is(err, model.ErrKeychainUnavailable) {
				t.Fatalf("Get error = %v, want ErrKeychainUnavailable", err)
			}
		})
	}
}

func TestKeychainOtherErrorsKeepCause(t *testing.T) {
	cause := errors.New("disk on fire")
	keyring.MockInitWithError(cause)
	t.Cleanup(keyring.MockInit)
	err := NewKeychain().Set(testServerID, testToken)
	if !errors.Is(err, cause) || errors.Is(err, model.ErrKeychainUnavailable) {
		t.Fatalf("Set error = %v, want the original cause only", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text leaks the token")
	}
}

func newTestFile(t *testing.T) (*File, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "butterdb-admin", "tokens")
	store, err := NewFile(dir)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	return store, dir
}

func TestFileStoreMode0600(t *testing.T) {
	store, dir := newTestFile(t)
	if err := store.Set(testServerID, testToken); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, testServerID+".token"))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("token dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	got, err := store.Get(testServerID)
	if err != nil || got != testToken {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if store.Kind() != model.TokenFile {
		t.Fatalf("Kind = %q", store.Kind())
	}
}

func TestFileStoreAtomicWrite(t *testing.T) {
	t.Run("overwrite leaves one file with the new token", func(t *testing.T) {
		store, dir := newTestFile(t)
		if err := store.Set(testServerID, "old-token"); err != nil {
			t.Fatalf("Set old: %v", err)
		}
		if err := store.Set(testServerID, testToken); err != nil {
			t.Fatalf("Set new: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != testServerID+".token" {
			t.Fatalf("directory entries = %v, want only the token file", names(entries))
		}
		if got, _ := store.Get(testServerID); got != testToken {
			t.Fatalf("Get = %q, want new token", got)
		}
	})
	t.Run("failed rename removes the temporary file", func(t *testing.T) {
		store, dir := newTestFile(t)
		if err := os.MkdirAll(filepath.Join(dir, testServerID+".token", "child"), 0o700); err != nil {
			t.Fatalf("seed blocking directory: %v", err)
		}
		if err := store.Set(testServerID, testToken); err == nil {
			t.Fatalf("Set succeeded over a directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("temporary file %q left behind", entry.Name())
			}
		}
	})
}

func TestFileStoreNotFoundAndDelete(t *testing.T) {
	store, _ := newTestFile(t)
	if _, err := store.Get(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Get error = %v, want ErrTokenNotFound", err)
	}
	if err := store.Delete(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Delete error = %v, want ErrTokenNotFound", err)
	}
	if err := store.Set(testServerID, testToken); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Delete(testServerID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}

func TestFileStoreRejectsUnsafeServerID(t *testing.T) {
	store, dir := newTestFile(t)
	for _, id := range []string{"", "../escape", "a/b", `a\b`, "a b", "."} {
		t.Run(id, func(t *testing.T) {
			if err := store.Set(id, testToken); !errors.Is(err, ErrInvalidServerID) {
				t.Fatalf("Set(%q) error = %v, want ErrInvalidServerID", id, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.token")); err == nil {
		t.Fatalf("escape file created")
	}
}

func TestTokenDirIn(t *testing.T) {
	got := TokenDirIn("/home/u/.config")
	want := filepath.Join("/home/u/.config", "butterdb-admin", "tokens")
	if got != want {
		t.Fatalf("TokenDirIn = %q, want %q", got, want)
	}
}

func TestMemoryStoreKind(t *testing.T) {
	store := NewMemory()
	if store.Kind() != model.TokenMemory {
		t.Fatalf("Kind = %q, want memory", store.Kind())
	}
	if _, err := store.Get(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("Get error = %v, want ErrTokenNotFound", err)
	}
	if err := store.Set(testServerID, testToken); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := store.Get(testServerID); err != nil || got != testToken {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete(testServerID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(testServerID); !errors.Is(err, model.ErrTokenNotFound) {
		t.Fatalf("second Delete error = %v", err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
