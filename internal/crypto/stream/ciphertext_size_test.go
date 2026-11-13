package stream

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
)

func TestCiphertextSizeMatchesEncryptStream(t *testing.T) {
	key := testKey(0x41)
	sizes := []int64{0, 1, 1000, 65535, 65536, 65537, 131072, 200000, 5000000}
	for _, n := range sizes {
		plain := deterministicBytes(t, n, int(n))
		encrypted := encryptToBytes(t, plain, key, testParams(t, n))
		if want := int(format.CiphertextSize(n)); len(encrypted) != want {
			t.Fatalf("size=%d: CiphertextSize=%d, EncryptStream wrote %d", n, want, len(encrypted))
		}
	}
}

func TestEncryptStreamRejectsSourceLengthMismatch(t *testing.T) {
	key := testKey(1)
	params := testParams(t, 1)
	tests := []struct {
		name         string
		plaintext    []byte
		declaredSize int64
	}{
		{name: "shorter than declared", plaintext: bytes.Repeat([]byte{0x12}, format.ChunkSize-1), declaredSize: format.ChunkSize},
		{name: "longer than declared", plaintext: append(bytes.Repeat([]byte{0x24}, format.ChunkSize), 0x25), declaredSize: format.ChunkSize},
		{name: "longer than declared partial final chunk", plaintext: bytes.Repeat([]byte{0x36}, 10), declaredSize: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EncryptStream(io.Discard, bytes.NewReader(tt.plaintext), key, tt.declaredSize, params.Salt, params.NoncePrefix)
			if !errors.Is(err, ErrPlaintextSizeMismatch) {
				t.Fatalf("EncryptStream length mismatch error = %v, want ErrPlaintextSizeMismatch", err)
			}
		})
	}
}

func TestEncryptStreamRejectsTrailingDataAfterEmptyRead(t *testing.T) {
	params := testParams(t, 2)
	source := &emptyThenTrailingReader{plaintext: []byte{0x12}, trailing: []byte{0x34}}
	err := EncryptStream(io.Discard, source, testKey(1), 1, params.Salt, params.NoncePrefix)
	if !errors.Is(err, ErrPlaintextSizeMismatch) {
		t.Fatalf("EncryptStream length mismatch error = %v, want ErrPlaintextSizeMismatch", err)
	}
}

func TestEncryptStreamRejectsUnendingEmptyTrailingSource(t *testing.T) {
	params := testParams(t, 3)
	source := &emptyThenTrailingReader{plaintext: []byte{0x12}, stall: true}
	err := EncryptStream(io.Discard, source, testKey(1), 1, params.Salt, params.NoncePrefix)
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("EncryptStream empty-read error = %v, want io.ErrNoProgress", err)
	}
}
