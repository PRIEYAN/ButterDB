package randomaccess

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
)

func TestTDE1TamperAndTruncation(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	plain := patternedPlaintext(3*format.ChunkSize + 41)
	valid := encryptRandomAccessFixture(t, plain, key)
	stride := format.ChunkSize + format.TagLen
	open := func(ciphertext []byte, storedSize int64) (*RandomAccessDecryptor, error) {
		return NewRandomAccessDecryptor(context.Background(), bytesContextReaderAt(ciphertext), storedSize, key)
	}

	t.Run("flipped byte fails only the touched chunk", func(t *testing.T) {
		for chunk := range 3 {
			tampered := append([]byte(nil), valid...)
			tampered[format.HeaderLen+chunk*stride+11] ^= 0x01
			decryptor, err := open(tampered, int64(len(tampered)))
			if err != nil {
				t.Fatalf("chunk %d: open: %v", chunk, err)
			}
			t.Cleanup(func() { _ = decryptor.Close() })
			for read := range 3 {
				got := make([]byte, 8)
				n, readErr := decryptor.ReadAt(context.Background(), got, int64(read*format.ChunkSize+3))
				if read == chunk {
					if n != 0 || !errors.Is(readErr, stream.ErrAuthFailed) {
						t.Fatalf("tampered chunk %d read = n %d err %v, want 0/ErrAuthFailed", chunk, n, readErr)
					}
					continue
				}
				if readErr != nil || !bytes.Equal(got[:n], plain[read*format.ChunkSize+3:read*format.ChunkSize+3+n]) {
					t.Fatalf("untouched chunk %d read = n %d err %v", read, n, readErr)
				}
			}
		}
	})

	t.Run("flipped byte in final chunk fails open", func(t *testing.T) {
		tampered := append([]byte(nil), valid...)
		tampered[len(tampered)-format.TagLen-1] ^= 0x01
		if decryptor, err := open(tampered, int64(len(tampered))); decryptor != nil || !errors.Is(err, stream.ErrAuthFailed) {
			t.Fatalf("open = %v, %v; want nil, ErrAuthFailed", decryptor, err)
		}
	})

	t.Run("swapped chunks fail", func(t *testing.T) {
		swapped := append([]byte(nil), valid...)
		first := swapped[format.HeaderLen : format.HeaderLen+stride]
		second := swapped[format.HeaderLen+stride : format.HeaderLen+2*stride]
		copy(first, valid[format.HeaderLen+stride:format.HeaderLen+2*stride])
		copy(second, valid[format.HeaderLen:format.HeaderLen+stride])
		decryptor, err := open(swapped, int64(len(swapped)))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = decryptor.Close() })
		if n, readErr := decryptor.ReadAt(context.Background(), make([]byte, 4), 0); n != 0 || !errors.Is(readErr, stream.ErrAuthFailed) {
			t.Fatalf("swapped read = n %d err %v, want 0/ErrAuthFailed", n, readErr)
		}
	})

	t.Run("truncated tail fails open", func(t *testing.T) {
		for _, cut := range []int{1, format.TagLen, 41 + format.TagLen, stride} {
			truncated := valid[:len(valid)-cut]
			if decryptor, err := open(truncated, int64(len(truncated))); decryptor != nil || !errors.Is(err, ErrCiphertextSize) {
				t.Fatalf("cut %d with honest size: open = %v, %v; want nil, ErrCiphertextSize", cut, decryptor, err)
			}
			if decryptor, err := open(truncated, int64(len(valid))); decryptor != nil || !errors.Is(err, stream.ErrAuthFailed) {
				t.Fatalf("cut %d with declared size: open = %v, %v; want nil, ErrAuthFailed", cut, decryptor, err)
			}
		}
	})

	t.Run("final chunk dropped and header size forged fails open", func(t *testing.T) {
		shortPlain := patternedPlaintext(2 * format.ChunkSize)
		fullStream := encryptRandomAccessFixture(t, shortPlain, key)
		withoutFinal := fullStream[:len(fullStream)-format.TagLen]
		if decryptor, err := open(withoutFinal, int64(len(withoutFinal))); decryptor != nil || err == nil {
			t.Fatalf("open without final chunk = %v, %v; want nil, error", decryptor, err)
		}
	})
}

func TestRandomAccessReadsEncryptingReaderAtOutput(t *testing.T) {
	key := bytes.Repeat([]byte{0x6b}, 32)
	plain := patternedPlaintext(2*format.ChunkSize + 77)
	var salt [format.SaltLen]byte
	var prefix [format.NoncePrefixLen]byte
	salt[0], prefix[0] = 9, 8
	encrypting, err := stream.NewEncryptingReaderAt(bytes.NewReader(plain), int64(len(plain)), key, salt, prefix)
	if err != nil {
		t.Fatalf("NewEncryptingReaderAt: %v", err)
	}
	t.Cleanup(func() { _ = encrypting.Close() })
	ciphertext := make([]byte, encrypting.Size())
	if _, err := encrypting.ReadAt(ciphertext, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	decryptor, err := NewRandomAccessDecryptor(context.Background(), bytesContextReaderAt(ciphertext), int64(len(ciphertext)), key)
	if err != nil {
		t.Fatalf("NewRandomAccessDecryptor: %v", err)
	}
	t.Cleanup(func() { _ = decryptor.Close() })
	got := make([]byte, len(plain))
	if n, err := decryptor.ReadAt(context.Background(), got, 0); err != nil || n != len(plain) || !bytes.Equal(got, plain) {
		t.Fatalf("ReadAt = n %d err %v, plaintext equal %t", n, err, bytes.Equal(got, plain))
	}
}
