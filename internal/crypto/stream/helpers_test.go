package stream

import (
	"bytes"
	"io"
	"math/rand"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
)

type shortNilWriter struct{}

func (shortNilWriter) Write(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	return len(buffer) - 1, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

type emptyThenTrailingReader struct {
	plaintext []byte
	trailing  []byte
	emitted   bool
	stall     bool
}

func (r *emptyThenTrailingReader) Read(buffer []byte) (int, error) {
	if len(r.plaintext) > 0 {
		readCount := copy(buffer, r.plaintext)
		r.plaintext = r.plaintext[readCount:]
		return readCount, nil
	}
	if !r.emitted {
		r.emitted = true
		return 0, nil
	}
	if r.stall {
		return 0, nil
	}
	if len(r.trailing) > 0 {
		readCount := copy(buffer, r.trailing)
		r.trailing = r.trailing[readCount:]
		return readCount, io.EOF
	}
	return 0, io.EOF
}

func deterministicBytes(t *testing.T, seed int64, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	if _, err := rand.New(rand.NewSource(seed)).Read(out); err != nil {
		t.Fatalf("deterministic bytes: %v", err)
	}
	return out
}

func testKey(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, KeyLen)
}

func testParams(t *testing.T, seed int64) FileParams {
	t.Helper()
	params, err := NewFileParams(bytes.NewReader(deterministicBytes(t, seed, format.SaltLen+format.NoncePrefixLen)))
	if err != nil {
		t.Fatalf("NewFileParams: %v", err)
	}
	return params
}

func encryptToBytes(t *testing.T, plaintext, key []byte, params FileParams) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := EncryptStream(&out, bytes.NewReader(plaintext), key, int64(len(plaintext)), params.Salt, params.NoncePrefix); err != nil {
		t.Fatalf("EncryptStream: %v", err)
	}
	return out.Bytes()
}
