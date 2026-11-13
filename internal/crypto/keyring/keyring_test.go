package keyring

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type countingReader struct {
	next byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next++
	}
	return len(p), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy exhausted")
}

func testRootKey() []byte {
	raw := make([]byte, keySize)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return raw
}

func writeKeyFile(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod key file: %v", err)
	}
	return path
}

func loadTestRing(t *testing.T, rand io.Reader) *Ring {
	t.Helper()
	ring, err := LoadWithRand(writeKeyFile(t, testRootKey()), rand)
	if err != nil {
		t.Fatalf("LoadWithRand: %v", err)
	}
	t.Cleanup(ring.Close)
	return ring
}

func referenceHKDF(root []byte, info string) []byte {
	extract := hmac.New(sha256.New, make([]byte, sha256.Size))
	extract.Write(root)
	prk := extract.Sum(nil)
	expand := hmac.New(sha256.New, prk)
	expand.Write([]byte(info))
	expand.Write([]byte{1})
	return expand.Sum(nil)
}

func TestKeyringDerivationsDistinct(t *testing.T) {
	root := testRootKey()
	ring := loadTestRing(t, &countingReader{})

	subkeys := []struct {
		name string
		info string
		got  []byte
	}{
		{"wrap", "butterdb/keyring/dek-wrap/v1", ring.wrap.Bytes()},
		{"secret", "butterdb/keyring/access-key-secret/v1", ring.secret.Bytes()},
		{"session", "butterdb/keyring/session-seal/v1", ring.session.Bytes()},
		{"snapshot", "butterdb/keyring/snapshot/v1", ring.snapshot.Bytes()},
	}
	seen := map[string]string{hex.EncodeToString(root): "server key"}
	for _, tc := range subkeys {
		t.Run(tc.name, func(t *testing.T) {
			want := referenceHKDF(root, tc.info)
			if !bytes.Equal(tc.got, want) {
				t.Fatalf("%s subkey does not equal HKDF-SHA256(empty salt, %q)", tc.name, tc.info)
			}
			id := hex.EncodeToString(tc.got)
			if prior, dup := seen[id]; dup {
				t.Fatalf("%s subkey equals %s", tc.name, prior)
			}
			seen[id] = tc.name
		})
	}

	fpFull := referenceHKDF(root, "butterdb/keyring/fingerprint/v1")
	fp := ring.Fingerprint()
	if len(fp) != 16 || !bytes.Equal(fp[:], fpFull[:16]) {
		t.Fatalf("fingerprint = %x, want first 16 bytes of %x", fp, fpFull)
	}
	if _, dup := seen[hex.EncodeToString(fpFull)]; dup {
		t.Fatal("fingerprint source equals another subkey")
	}
	if got := ring.FingerprintString(); got != "bkf1_"+hex.EncodeToString(fpFull[:16]) {
		t.Fatalf("FingerprintString = %q", got)
	}
	if !regexp.MustCompile(`^bkf1_[0-9a-f]{32}$`).MatchString(ring.FingerprintString()) {
		t.Fatalf("FingerprintString %q is not bkf1_ plus 32 lowercase hex characters", ring.FingerprintString())
	}

	mac := hmac.New(sha256.New, root)
	mac.Write([]byte("butterdb/s3/owner-id/v1"))
	if got, want := ring.OwnerID(), hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("OwnerID = %q, want %q", got, want)
	}
}

func TestKeyFileLengthExact(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{name: "empty", size: 0, wantErr: ErrKeyFileLength},
		{name: "31 bytes", size: 31, wantErr: ErrKeyFileLength},
		{name: "32 bytes", size: 32},
		{name: "33 bytes", size: 33, wantErr: ErrKeyFileLength},
		{name: "64 bytes", size: 64, wantErr: ErrKeyFileLength},
		{name: "1 MiB", size: 1 << 20, wantErr: ErrKeyFileLength},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, bytes.Repeat([]byte{0x5a}, tc.size))
			ring, err := Load(path)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Load error = %v, want %v", err, tc.wantErr)
				}
				if ring != nil {
					t.Fatal("Load returned a ring with an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			ring.Close()
		})
	}

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "absent.key")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load error = %v, want not exist", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		if _, err := Load(t.TempDir()); !errors.Is(err, ErrKeyFileNotRegular) {
			t.Fatalf("Load error = %v, want ErrKeyFileNotRegular", err)
		}
	})
}

func TestGenerateWritesLoadableKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets", "server.key")

	fp, err := Generate(path, &countingReader{next: 7})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated key: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("generated key length = %d, want 32", len(raw))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated key mode = %v, want 0600", info.Mode().Perm())
	}
	ring, err := Load(path)
	if err != nil {
		t.Fatalf("Load generated key: %v", err)
	}
	defer ring.Close()
	if ring.Fingerprint() != fp {
		t.Fatalf("loaded fingerprint %x differs from generated %x", ring.Fingerprint(), fp)
	}

	if _, err := Generate(path, &countingReader{next: 99}); !errors.Is(err, ErrKeyFileExists) {
		t.Fatalf("Generate over existing key = %v, want ErrKeyFileExists", err)
	}
	again, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(again, raw) {
		t.Fatalf("existing key file was modified: err=%v", err)
	}
}

func TestGenerateRejectsBadRandomness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.key")
	if _, err := Generate(path, nil); !errors.Is(err, ErrNilRandom) {
		t.Fatalf("Generate nil random = %v, want ErrNilRandom", err)
	}
	if _, err := Generate(path, failingReader{}); err == nil {
		t.Fatal("Generate with failing random succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key file exists after failed Generate: %v", err)
	}
	if _, err := LoadWithRand(path, nil); !errors.Is(err, ErrNilRandom) {
		t.Fatalf("LoadWithRand nil random = %v, want ErrNilRandom", err)
	}
}

func TestKeyRedactsInLogs(t *testing.T) {
	raw := testRootKey()
	key, err := NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	ring := loadTestRing(t, &countingReader{})
	forbidden := []string{
		hex.EncodeToString(raw),
		base64.StdEncoding.EncodeToString(raw),
		hex.EncodeToString(ring.wrap.Bytes()),
		hex.EncodeToString(ring.secret.Bytes()),
		fmt.Sprint(raw),
	}

	var jsonBuf, textBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonBuf, nil)).Info("keys", "value", *key, "pointer", key, "ring", ring)
	slog.New(slog.NewTextHandler(&textBuf, nil)).Info("keys", "value", *key, "pointer", key, "ring", ring)

	outputs := map[string]string{"json": jsonBuf.String(), "text": textBuf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
		outputs["value "+verb] = fmt.Sprintf(verb, *key)
		outputs["pointer "+verb] = fmt.Sprintf(verb, key)
		outputs["ring "+verb] = fmt.Sprintf(verb, ring)
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			for _, secret := range forbidden {
				if strings.Contains(out, secret) {
					t.Fatalf("output %q leaks key material", out)
				}
			}
		})
	}
	if !strings.Contains(jsonBuf.String(), "[REDACTED]") {
		t.Fatalf("json log %q lacks redaction marker", jsonBuf.String())
	}
	if got := key.LogValue().String(); got != "[REDACTED]" {
		t.Fatalf("LogValue = %q", got)
	}
}

func TestKeyOwnsIndependentCopy(t *testing.T) {
	raw := testRootKey()
	key, err := NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	raw[0] ^= 0xff
	if key.Bytes()[0] == raw[0] {
		t.Fatal("key aliases the caller's input")
	}
	if _, err := NewKey(raw[:31]); !errors.Is(err, ErrKeyLength) {
		t.Fatalf("NewKey short = %v, want ErrKeyLength", err)
	}
	if _, err := NewKey(append(raw, 0)); !errors.Is(err, ErrKeyLength) {
		t.Fatalf("NewKey long = %v, want ErrKeyLength", err)
	}
}

func TestKeyClearIsIdempotentAndNilSafe(t *testing.T) {
	key, err := GenerateKey(&countingReader{next: 1})
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	view := key.Bytes()
	key.Clear()
	key.Clear()
	if !bytes.Equal(view, make([]byte, keySize)) {
		t.Fatal("Clear left key bytes behind")
	}
	var missing *Key
	missing.Clear()
	if missing.Bytes() != nil {
		t.Fatal("nil key returned bytes")
	}
	if _, err := GenerateKey(failingReader{}); err == nil {
		t.Fatal("GenerateKey with failing random succeeded")
	}
	if _, err := GenerateKey(nil); !errors.Is(err, ErrNilRandom) {
		t.Fatalf("GenerateKey nil random = %v", err)
	}
}

func TestNewKeyID(t *testing.T) {
	kid, err := NewKeyID(&countingReader{next: 0xa0})
	if err != nil {
		t.Fatalf("NewKeyID: %v", err)
	}
	if kid != "a0a1a2a3a4a5a6a7" || !validKeyID(kid) {
		t.Fatalf("kid = %q", kid)
	}
	for _, bad := range []string{"", "A0A1A2A3A4A5A6A7", "a0a1a2a3a4a5a6a", "a0a1a2a3a4a5a6a7a", "g0a1a2a3a4a5a6a7"} {
		if validKeyID(bad) {
			t.Fatalf("validKeyID(%q) = true", bad)
		}
	}
	if _, err := NewKeyID(failingReader{}); err == nil {
		t.Fatal("NewKeyID with failing random succeeded")
	}
}

func TestRingLeasesAreIndependentAndCloseZeroesKeys(t *testing.T) {
	ring := loadTestRing(t, &countingReader{})
	wantSession := append([]byte(nil), ring.session.Bytes()...)

	lease := ring.SessionKey()
	if !bytes.Equal(lease.Bytes(), wantSession) {
		t.Fatal("session lease differs from the derived key")
	}
	lease.Clear()
	if !bytes.Equal(ring.session.Bytes(), wantSession) {
		t.Fatal("clearing a lease cleared the ring's own key")
	}
	other := ring.SessionKey()
	if &other.Bytes()[0] == &ring.session.Bytes()[0] {
		t.Fatal("lease aliases the ring's key")
	}
	snap := ring.SnapshotKey()
	if bytes.Equal(snap.Bytes(), other.Bytes()) {
		t.Fatal("session and snapshot leases are equal")
	}

	ring.Close()
	ring.Close()
	zero := make([]byte, keySize)
	for name, key := range map[string]*Key{"wrap": &ring.wrap, "secret": &ring.secret, "session": &ring.session, "snapshot": &ring.snapshot} {
		if !bytes.Equal(key.Bytes(), zero) {
			t.Fatalf("%s key survives Close", name)
		}
	}
	if ring.SessionKey() != nil || ring.SnapshotKey() != nil {
		t.Fatal("closed ring handed out a key")
	}
	if ring.FingerprintString() == "" || ring.OwnerID() == "" {
		t.Fatal("public identifiers vanished after Close")
	}
}
