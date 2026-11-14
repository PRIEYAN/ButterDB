package keyring

import (
	"bytes"
	"errors"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

const testAccessKeyID = "BDBKABCDEFGHIJ0123456"

func TestSealSecretBindsAccessKeyID(t *testing.T) {
	root := testRootKey()
	ring := loadTestRing(t, &countingReader{next: 40})
	secret := bytes.Repeat([]byte("s3cretvalue"), 4)[:40]

	sealed, err := ring.SealSecret(testAccessKeyID, secret)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if len(sealed) != 80 {
		t.Fatalf("sealed length = %d, want 80", len(sealed))
	}
	if bytes.Contains(sealed, secret[:8]) {
		t.Fatal("sealed value contains plaintext")
	}

	aead, err := chacha20poly1305.NewX(referenceHKDF(root, "butterdb/keyring/access-key-secret/v1"))
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	plain, err := aead.Open(nil, sealed[:24], sealed[24:], []byte("butterdb/access/v1|"+testAccessKeyID))
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatalf("independent open failed: %v", err)
	}

	tests := []struct {
		name    string
		ring    *Ring
		id      string
		sealed  []byte
		wantErr error
	}{
		{name: "same key id", ring: ring, id: testAccessKeyID, sealed: sealed},
		{name: "other key id", ring: ring, id: "BDBKZZZZZZZZZZZZZZZZ", sealed: sealed, wantErr: ErrSecretUnseal},
		{name: "key id prefix", ring: ring, id: testAccessKeyID[:len(testAccessKeyID)-1], sealed: sealed, wantErr: ErrSecretUnseal},
		{name: "other server key", ring: otherRing(t), id: testAccessKeyID, sealed: sealed, wantErr: ErrSecretUnseal},
		{name: "truncated", ring: ring, id: testAccessKeyID, sealed: sealed[:79], wantErr: ErrSecretUnseal},
		{name: "extended", ring: ring, id: testAccessKeyID, sealed: append(append([]byte(nil), sealed...), 0), wantErr: ErrSecretUnseal},
		{name: "empty", ring: ring, id: testAccessKeyID, sealed: nil, wantErr: ErrSecretUnseal},
		{name: "nonce only", ring: ring, id: testAccessKeyID, sealed: sealed[:24], wantErr: ErrSecretUnseal},
		{name: "empty key id", ring: ring, id: "", sealed: sealed, wantErr: ErrEmptyAccessKeyID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.ring.OpenSecret(tc.id, tc.sealed)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatal("plaintext returned with an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenSecret: %v", err)
			}
			if !bytes.Equal(got, secret) {
				t.Fatal("opened secret differs")
			}
		})
	}

	t.Run("tampered byte", func(t *testing.T) {
		for _, i := range []int{0, 24, 63, 79} {
			tampered := append([]byte(nil), sealed...)
			tampered[i] ^= 0x01
			if _, err := ring.OpenSecret(testAccessKeyID, tampered); !errors.Is(err, ErrSecretUnseal) {
				t.Fatalf("byte %d: error = %v, want ErrSecretUnseal", i, err)
			}
		}
	})
}

func TestSealSecretRejectsBadInputs(t *testing.T) {
	ring := loadTestRing(t, &countingReader{})
	if _, err := ring.SealSecret(testAccessKeyID, nil); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("empty secret = %v, want ErrEmptySecret", err)
	}
	if _, err := ring.SealSecret("", []byte("x")); !errors.Is(err, ErrEmptyAccessKeyID) {
		t.Fatalf("empty id = %v, want ErrEmptyAccessKeyID", err)
	}
	failing := loadTestRing(t, failingReader{})
	if sealed, err := failing.SealSecret(testAccessKeyID, []byte("x")); err == nil || sealed != nil {
		t.Fatalf("SealSecret with failing random = %v, %v", sealed, err)
	}
}

func TestSealSecretUsesFreshNonces(t *testing.T) {
	ring := loadTestRing(t, &countingReader{})
	first, err := ring.SealSecret(testAccessKeyID, []byte("same secret"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	second, err := ring.SealSecret(testAccessKeyID, []byte("same secret"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if bytes.Equal(first[:24], second[:24]) || bytes.Equal(first, second) {
		t.Fatal("two seals share a nonce")
	}
}
