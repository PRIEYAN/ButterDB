package keyring

import (
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
)

func TestEscrowRoundTripAndWrongPassword(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "server.key")
	if _, err := Generate(src, rand.Reader); err != nil {
		t.Fatal(err)
	}
	want, err := KeyFileFingerprint(src)
	if err != nil {
		t.Fatal(err)
	}
	params := EscrowParams{Time: 1, MemoryKiB: 19456, Parallel: 1}
	e, err := SealEscrowFile(src, []byte("correct horse battery"), "dep1", params, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if e.Fingerprint != want {
		t.Fatalf("fingerprint %s want %s", e.Fingerprint, want)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := OpenEscrowToFile(e, []byte("wrong password!!"), "dep1", bad); !errors.Is(err, ErrEscrowPassword) {
		t.Fatalf("wrong password err = %v", err)
	}
	if _, err := KeyFileFingerprint(bad); err == nil {
		t.Fatal("wrong password wrote a key file")
	}
	if err := OpenEscrowToFile(e, []byte("correct horse battery"), "dep2", bad); !errors.Is(err, ErrEscrowPassword) {
		t.Fatalf("other deployment err = %v", err)
	}
	dst := filepath.Join(dir, "copy.key")
	if err := OpenEscrowToFile(e, []byte("correct horse battery"), "dep1", dst); err != nil {
		t.Fatal(err)
	}
	got, err := KeyFileFingerprint(dst)
	if err != nil || got != want {
		t.Fatalf("restored fingerprint %s err %v", got, err)
	}
}
