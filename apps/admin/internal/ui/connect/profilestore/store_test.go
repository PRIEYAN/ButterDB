package profilestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func sampleProfile(serverID, label string) model.Profile {
	at := time.Date(2026, 11, 12, 9, 41, 7, 312_000_000, time.UTC)
	return model.Profile{
		ServerID:      serverID,
		Label:         label,
		BaseURL:       "https://203.0.113.10:9443",
		Trust:         adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: "SHA256:4f1c9a0e"},
		DeviceID:      "a1b2c3d4e5f6g7h2",
		DeviceName:    "Office laptop",
		TokenStorage:  model.TokenKeychain,
		TokenIssuedAt: at,
		AddedAt:       at,
		LastUsedAt:    at.Add(24 * time.Hour),
	}
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "butterdb-admin", "servers.json")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, path
}

func TestProfilesRoundTrip(t *testing.T) {
	store, path := newStore(t)
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty List = %v, %v", list, err)
	}
	if id, err := store.LastUsed(); err != nil || id != "" {
		t.Fatalf("empty LastUsed = %q, %v", id, err)
	}
	first := sampleProfile("server-one", "home server")
	second := sampleProfile("server-two", "office")
	for _, p := range []model.Profile{first, second} {
		if err := store.Put(p); err != nil {
			t.Fatalf("Put %s: %v", p.ServerID, err)
		}
	}
	if err := store.SetLastUsed("server-two"); err != nil {
		t.Fatalf("SetLastUsed: %v", err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatalf("New reopen: %v", err)
	}
	list, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(list, []model.Profile{first, second}) {
		t.Fatalf("List = %+v", list)
	}
	got, err := reopened.Get("server-one")
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if id, err := reopened.LastUsed(); err != nil || id != "server-two" {
		t.Fatalf("LastUsed = %q, %v", id, err)
	}
	if err := reopened.Delete("server-two"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if id, _ := reopened.LastUsed(); id != "" {
		t.Fatalf("LastUsed after deleting it = %q, want empty", id)
	}
	if _, err := reopened.Get("server-two"); !errors.Is(err, model.ErrProfileNotFound) {
		t.Fatalf("Get deleted error = %v", err)
	}
}

func TestProfilesFileMode(t *testing.T) {
	store, path := newStore(t)
	if err := store.Put(sampleProfile("server-one", "home")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only servers.json", entries, err)
	}
}

func TestProfilesKeyedByServerID(t *testing.T) {
	store, path := newStore(t)
	original := sampleProfile("server-one", "home")
	if err := store.Put(original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	moved := original
	moved.BaseURL = "https://198.51.100.4:9443"
	moved.Label = "home via vpn"
	if err := store.Put(moved); err != nil {
		t.Fatalf("Put moved: %v", err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v; want one profile", list, err)
	}
	if list[0].BaseURL != moved.BaseURL || list[0].DeviceID != original.DeviceID {
		t.Fatalf("profile = %+v", list[0])
	}
	if err := store.SetLastUsed("unknown"); !errors.Is(err, model.ErrProfileNotFound) {
		t.Fatalf("SetLastUsed unknown error = %v", err)
	}
	if err := store.Delete("unknown"); !errors.Is(err, model.ErrProfileNotFound) {
		t.Fatalf("Delete unknown error = %v", err)
	}
	if err := store.Put(model.Profile{}); err == nil {
		t.Fatalf("Put with empty server id succeeded")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		t.Fatalf("file version = %d, %v", doc.Version, err)
	}
	if !strings.Contains(string(raw), `"spki_sha256": "SHA256:4f1c9a0e"`) {
		t.Fatalf("trust object missing from file: %s", raw)
	}
}

func TestProfilesRejectUnknownVersion(t *testing.T) {
	store, path := newStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("List error = %v, want ErrUnsupportedVersion", err)
	}
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatalf("List accepted a corrupt file")
	}
}

func TestPathIn(t *testing.T) {
	got := PathIn("/home/u/.config")
	want := filepath.Join("/home/u/.config", "butterdb-admin", "servers.json")
	if got != want {
		t.Fatalf("PathIn = %q, want %q", got, want)
	}
}
