package keyring

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"golang.org/x/crypto/chacha20poly1305"
)

const testKID = "0123456789abcdef"

func otherRing(t *testing.T) *Ring {
	t.Helper()
	raw := bytes.Repeat([]byte{0x77}, keySize)
	path := filepath.Join(t.TempDir(), "other.key")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	ring, err := LoadWithRand(path, &countingReader{})
	if err != nil {
		t.Fatalf("LoadWithRand: %v", err)
	}
	t.Cleanup(ring.Close)
	return ring
}

func testDEK(t *testing.T, seed byte) *Key {
	t.Helper()
	dek, err := GenerateKey(&countingReader{next: seed})
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return dek
}

func TestKeyWrapRoundTrip(t *testing.T) {
	ring := loadTestRing(t, &countingReader{next: 100})
	dek := testDEK(t, 1)
	wrap, check, err := ring.WrapDEK(dek, -1001234567, testKID)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if len(wrap) != 72 || len(check) != 61 {
		t.Fatalf("sizes = %d, %d, want 72, 61", len(wrap), len(check))
	}

	got, err := ring.UnwrapDEK(wrap[:], check[:], -1001234567, testKID)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if !bytes.Equal(got.Bytes(), dek.Bytes()) {
		t.Fatal("unwrapped dek does not match")
	}
	got.Clear()

	t.Run("wrong server key", func(t *testing.T) {
		if _, err := otherRing(t).UnwrapDEK(wrap[:], check[:], -1001234567, testKID); !errors.Is(err, ErrDEKUnwrap) {
			t.Fatalf("error = %v, want ErrDEKUnwrap", err)
		}
	})
	t.Run("tampered wrap", func(t *testing.T) {
		for _, i := range []int{0, 23, 24, 71} {
			tampered := append([]byte(nil), wrap[:]...)
			tampered[i] ^= 0x01
			if _, err := ring.UnwrapDEK(tampered, check[:], -1001234567, testKID); !errors.Is(err, ErrDEKUnwrap) {
				t.Fatalf("byte %d: error = %v, want ErrDEKUnwrap", i, err)
			}
		}
	})
	t.Run("tampered key check", func(t *testing.T) {
		for _, i := range []int{0, 23, 24, 60} {
			tampered := append([]byte(nil), check[:]...)
			tampered[i] ^= 0x01
			if _, err := ring.UnwrapDEK(wrap[:], tampered, -1001234567, testKID); !errors.Is(err, ErrDEKCheck) {
				t.Fatalf("byte %d: error = %v, want ErrDEKCheck", i, err)
			}
		}
	})
	t.Run("errors map to key unavailable", func(t *testing.T) {
		_, err := otherRing(t).UnwrapDEK(wrap[:], check[:], -1001234567, testKID)
		if !errors.Is(err, errs.ErrKeyUnavailable) || errs.Code(err) != errs.CodeKeyUnavailable {
			t.Fatalf("error %v does not map to key unavailable", err)
		}
		if errors.Is(ErrDEKUnwrap, ErrDEKCheck) || errors.Is(ErrDEKCheck, ErrDEKUnwrap) {
			t.Fatal("unwrap and check sentinels are not distinct")
		}
	})
}

func TestWrapDEKLayoutAndAssociatedData(t *testing.T) {
	root := testRootKey()
	ring := loadTestRing(t, &countingReader{next: 10})
	dek := testDEK(t, 50)
	wrap, check, err := ring.WrapDEK(dek, -1001234567, testKID)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}

	wrapNonce := make([]byte, 24)
	for i := range wrapNonce {
		wrapNonce[i] = byte(10 + i)
	}
	if !bytes.Equal(wrap[:24], wrapNonce) {
		t.Fatal("wrap does not start with the injected nonce")
	}
	wrapAEAD, err := chacha20poly1305.NewX(referenceHKDF(root, "butterdb/keyring/dek-wrap/v1"))
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	plain, err := wrapAEAD.Open(nil, wrap[:24], wrap[24:], []byte("butterdb/dek/v1|-1001234567|0123456789abcdef"))
	if err != nil || !bytes.Equal(plain, dek.Bytes()) {
		t.Fatalf("independent unwrap failed: %v", err)
	}

	checkAEAD, err := chacha20poly1305.NewX(dek.Bytes())
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	marker, err := checkAEAD.Open(nil, check[:24], check[24:], nil)
	if err != nil || string(marker) != "butterDB-key-check-v1" {
		t.Fatalf("independent key check open = %q, %v", marker, err)
	}
	if len("butterDB-key-check-v1") != 21 {
		t.Fatal("key check plaintext is not 21 bytes")
	}
}

func TestWrappedDEKBoundToBucket(t *testing.T) {
	ring := loadTestRing(t, &countingReader{next: 3})
	wrap, check, err := ring.WrapDEK(testDEK(t, 9), -1001000000001, testKID)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	otherWrap, otherCheck, err := ring.WrapDEK(testDEK(t, 90), -1001000000001, testKID)
	if err != nil {
		t.Fatalf("WrapDEK other: %v", err)
	}

	tests := []struct {
		name    string
		wrap    []byte
		check   []byte
		channel int64
		kid     string
		wantErr error
	}{
		{name: "same bucket and kid", wrap: wrap[:], check: check[:], channel: -1001000000001, kid: testKID},
		{name: "other channel", wrap: wrap[:], check: check[:], channel: -1001000000002, kid: testKID, wantErr: ErrDEKUnwrap},
		{name: "positive channel", wrap: wrap[:], check: check[:], channel: 1001000000001, kid: testKID, wantErr: ErrDEKUnwrap},
		{name: "other kid", wrap: wrap[:], check: check[:], channel: -1001000000001, kid: "fedcba9876543210", wantErr: ErrDEKUnwrap},
		{name: "malformed kid", wrap: wrap[:], check: check[:], channel: -1001000000001, kid: "ZZ", wantErr: ErrDEKUnwrap},
		{name: "check from another dek", wrap: wrap[:], check: otherCheck[:], channel: -1001000000001, kid: testKID, wantErr: ErrDEKCheck},
		{name: "wrap from another dek", wrap: otherWrap[:], check: check[:], channel: -1001000000001, kid: testKID, wantErr: ErrDEKCheck},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ring.UnwrapDEK(tc.wrap, tc.check, tc.channel, tc.kid)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatal("key returned with an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("UnwrapDEK: %v", err)
			}
			got.Clear()
		})
	}
}

func TestVaultBlobsRequireExactAuthenticatedEnvelopeSizes(t *testing.T) {
	ring := loadTestRing(t, &countingReader{})
	wrap, check, err := ring.WrapDEK(testDEK(t, 4), 77, testKID)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	for _, size := range []int{0, len(wrap) - 1, len(wrap) + 1, 1 << 20} {
		if _, err := ring.UnwrapDEK(bytes.Repeat([]byte{1}, size), check[:], 77, testKID); !errors.Is(err, ErrDEKUnwrap) {
			t.Errorf("wrap size %d error = %v, want ErrDEKUnwrap", size, err)
		}
	}
	for _, size := range []int{0, len(check) - 1, len(check) + 1, 1 << 20} {
		if _, err := ring.UnwrapDEK(wrap[:], bytes.Repeat([]byte{1}, size), 77, testKID); !errors.Is(err, ErrDEKCheck) {
			t.Errorf("check size %d error = %v, want ErrDEKCheck", size, err)
		}
	}
	if _, err := ring.UnwrapDEK(nil, nil, 77, testKID); !errors.Is(err, ErrDEKUnwrap) {
		t.Errorf("nil blobs error = %v, want ErrDEKUnwrap", err)
	}
}

func TestKeyCopiesCleared(t *testing.T) {
	ring := loadTestRing(t, &countingReader{next: 20})
	dek := testDEK(t, 8)
	wantDEK := append([]byte(nil), dek.Bytes()...)
	wrap, check, err := ring.WrapDEK(dek, 5, testKID)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if !bytes.Equal(dek.Bytes(), wantDEK) {
		t.Fatal("WrapDEK modified the caller's dek")
	}
	zero := make([]byte, keySize)

	tests := []struct {
		name    string
		wrap    []byte
		check   []byte
		channel int64
		kid     string
		wantErr error
	}{
		{name: "check failure after unwrap", wrap: wrap[:], check: bytes.Repeat([]byte{9}, 61), channel: 5, kid: testKID, wantErr: ErrDEKCheck},
		{name: "short check after unwrap", wrap: wrap[:], check: check[:60], channel: 5, kid: testKID, wantErr: ErrDEKCheck},
		{name: "wrong bucket", wrap: wrap[:], check: check[:], channel: 6, kid: testKID, wantErr: ErrDEKUnwrap},
		{name: "wrong size wrap", wrap: wrap[:71], check: check[:], channel: 5, kid: testKID, wantErr: ErrDEKUnwrap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scratch := &Key{}
			for i := range scratch.b {
				scratch.b[i] = 0xee
			}
			err := ring.unwrapInto(scratch, tc.wrap, tc.check, tc.channel, tc.kid)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if !bytes.Equal(scratch.Bytes(), zero) {
				t.Fatal("dek bytes remain after a failed unwrap")
			}
		})
	}

	t.Run("success then clear", func(t *testing.T) {
		got, err := ring.UnwrapDEK(wrap[:], check[:], 5, testKID)
		if err != nil {
			t.Fatalf("UnwrapDEK: %v", err)
		}
		view := got.Bytes()
		got.Clear()
		if !bytes.Equal(view, zero) {
			t.Fatal("Clear left dek bytes behind")
		}
	})

	t.Run("failed wrap returns zero blobs", func(t *testing.T) {
		failing := loadTestRing(t, failingReader{})
		w, c, err := failing.WrapDEK(dek, 5, testKID)
		if err == nil {
			t.Fatal("WrapDEK with failing random succeeded")
		}
		if w != [72]byte{} || c != [61]byte{} {
			t.Fatal("blobs returned with an error")
		}
		if _, _, err := ring.WrapDEK(dek, 5, "bad"); !errors.Is(err, ErrInvalidKeyID) {
			t.Fatalf("WrapDEK bad kid = %v, want ErrInvalidKeyID", err)
		}
		if _, _, err := ring.WrapDEK(nil, 5, testKID); !errors.Is(err, ErrNilKey) {
			t.Fatalf("WrapDEK nil dek = %v, want ErrNilKey", err)
		}
	})

	t.Run("closed ring", func(t *testing.T) {
		closed := loadTestRing(t, &countingReader{})
		closed.Close()
		if _, _, err := closed.WrapDEK(dek, 5, testKID); !errors.Is(err, ErrRingClosed) {
			t.Fatalf("WrapDEK on closed ring = %v, want ErrRingClosed", err)
		}
		scratch := testDEK(t, 1)
		if err := closed.unwrapInto(scratch, wrap[:], check[:], 5, testKID); !errors.Is(err, ErrRingClosed) {
			t.Fatalf("unwrap on closed ring = %v, want ErrRingClosed", err)
		}
		if !bytes.Equal(scratch.Bytes(), zero) {
			t.Fatal("scratch key not cleared on closed ring")
		}
		if _, err := closed.SealSecret("BDBKX", []byte("s")); !errors.Is(err, ErrRingClosed) {
			t.Fatalf("SealSecret on closed ring = %v, want ErrRingClosed", err)
		}
	})
}

func TestRingConcurrentUseWithClose(t *testing.T) {
	ring := loadTestRing(t, &countingReader{})
	dek := testDEK(t, 2)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				wrap, check, err := ring.WrapDEK(dek, 9, testKID)
				if err != nil {
					if !errors.Is(err, ErrRingClosed) {
						t.Errorf("WrapDEK: %v", err)
					}
					return
				}
				got, err := ring.UnwrapDEK(wrap[:], check[:], 9, testKID)
				if err != nil {
					if !errors.Is(err, ErrRingClosed) {
						t.Errorf("UnwrapDEK: %v", err)
					}
					return
				}
				got.Clear()
				ring.SessionKey().Clear()
			}
		}()
	}
	ring.Close()
	wg.Wait()
}
