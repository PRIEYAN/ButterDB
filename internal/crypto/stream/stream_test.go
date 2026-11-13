package stream

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func TestStreamRoundTrip(t *testing.T) {
	key := testKey(0x11)
	sizes := []int{0, 1, format.ChunkSize - 1, format.ChunkSize, format.ChunkSize + 1, format.ChunkSize*3 + 17, 1 << 20}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("size-%d", n), func(t *testing.T) {
			plain := deterministicBytes(t, int64(n), n)
			encrypted := encryptToBytes(t, plain, key, testParams(t, 7))
			var decrypted bytes.Buffer
			size, err := DecryptStream(&decrypted, bytes.NewReader(encrypted), key)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if size != int64(n) {
				t.Fatalf("declared plaintext size %d, want %d", size, n)
			}
			if !bytes.Equal(decrypted.Bytes(), plain) {
				t.Fatal("round trip mismatch")
			}
		})
	}
}

func TestStreamKnownAnswerVector(t *testing.T) {
	key := testKey(0x33)
	params := testParams(t, 99)
	plain := []byte("butterDB known answer vector")
	first := encryptToBytes(t, plain, key, params)
	second := encryptToBytes(t, plain, key, params)
	if !bytes.Equal(first, second) {
		t.Fatal("same inputs produced different ciphertext")
	}
	if got, want := len(first), int(format.CiphertextSize(int64(len(plain)))); got != want {
		t.Fatalf("ciphertext length %d, want %d", got, want)
	}
	header, err := format.DecodeHeader(first)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if header.Salt != params.Salt || header.NoncePrefix != params.NoncePrefix || header.PlaintextSize != int64(len(plain)) {
		t.Fatal("header does not carry the supplied salt, prefix and size")
	}
}

func TestStreamRejectsWrongKey(t *testing.T) {
	plain := []byte("hello world")
	encrypted := encryptToBytes(t, plain, testKey(1), testParams(t, 1))
	var decrypted bytes.Buffer
	if _, err := DecryptStream(&decrypted, bytes.NewReader(encrypted), testKey(2)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestStreamRejectsTamperedChunk(t *testing.T) {
	key := testKey(3)
	plain := bytes.Repeat([]byte{0xAB}, format.ChunkSize*2+5)
	encrypted := encryptToBytes(t, plain, key, testParams(t, 2))
	encrypted[format.HeaderLen+1] ^= 0x80
	var decrypted bytes.Buffer
	if _, err := DecryptStream(&decrypted, bytes.NewReader(encrypted), key); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestStreamRejectsTruncated(t *testing.T) {
	key := testKey(4)
	plain := bytes.Repeat([]byte{0xCD}, format.ChunkSize*3)
	encrypted := encryptToBytes(t, plain, key, testParams(t, 3))
	truncated := encrypted[:len(encrypted)-32]
	var decrypted bytes.Buffer
	if _, err := DecryptStream(&decrypted, bytes.NewReader(truncated), key); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestStreamLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-file test in -short mode")
	}
	key := testKey(5)
	params := testParams(t, 4)
	const size = 50 * 1024 * 1024
	var encrypted bytes.Buffer
	if err := EncryptStream(&encrypted, io.LimitReader(zeroReader{}, size), key, size, params.Salt, params.NoncePrefix); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var sink countWriter
	got, err := DecryptStream(&sink, &encrypted, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != size || sink.n != size {
		t.Fatalf("plaintext size %d, written %d, want %d", got, sink.n, int64(size))
	}
}

func TestEncryptStreamRejectsCounterOverflow(t *testing.T) {
	params := testParams(t, 5)
	err := EncryptStream(io.Discard, bytes.NewReader(nil), testKey(6), format.MaxPlaintextSize+1, params.Salt, params.NoncePrefix)
	if !errors.Is(err, format.ErrPlaintextTooLarge) {
		t.Fatalf("EncryptStream beyond counter capacity error = %v, want ErrPlaintextTooLarge", err)
	}
}

func TestStreamRejectsInvalidMasterKeyLengths(t *testing.T) {
	t.Parallel()
	params := testParams(t, 6)
	for _, size := range []int{0, 31, 33, 128} {
		t.Run(fmt.Sprintf("key-%d", size), func(t *testing.T) {
			t.Parallel()
			key := bytes.Repeat([]byte{1}, size)
			if err := EncryptStream(io.Discard, bytes.NewReader(nil), key, 0, params.Salt, params.NoncePrefix); !errors.Is(err, ErrInvalidMasterKey) {
				t.Fatalf("EncryptStream key length %d error = %v, want ErrInvalidMasterKey", size, err)
			}
			if _, err := DecryptStream(io.Discard, bytes.NewReader(make([]byte, format.HeaderLen)), key); !errors.Is(err, ErrInvalidMasterKey) {
				t.Fatalf("DecryptStream key length %d error = %v, want ErrInvalidMasterKey", size, err)
			}
		})
	}
}

func TestStreamRejectsShortWrites(t *testing.T) {
	t.Parallel()
	key := testKey(1)
	params := testParams(t, 7)
	if err := EncryptStream(shortNilWriter{}, bytes.NewReader(nil), key, 0, params.Salt, params.NoncePrefix); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("EncryptStream short header write error = %v, want io.ErrShortWrite", err)
	}
	plaintext := []byte("secret")
	encrypted := encryptToBytes(t, plaintext, key, params)
	if _, err := DecryptStream(shortNilWriter{}, bytes.NewReader(encrypted), key); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("DecryptStream short plaintext write error = %v, want io.ErrShortWrite", err)
	}
}

func TestDecryptStreamRejectsUnsupportedFlagsAndImpossibleSize(t *testing.T) {
	t.Parallel()
	key := testKey(2)
	encrypted := encryptToBytes(t, nil, key, testParams(t, 8))
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "reserved flags", mutate: func(stream []byte) { stream[4] = 1 }},
		{name: "uint64 size", mutate: func(stream []byte) { binary.LittleEndian.PutUint64(stream[42:50], math.MaxUint64) }},
		{name: "counter capacity", mutate: func(stream []byte) {
			binary.LittleEndian.PutUint64(stream[42:50], uint64(format.MaxPlaintextSize)+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stream := append([]byte(nil), encrypted...)
			test.mutate(stream)
			var plaintext bytes.Buffer
			if _, err := DecryptStream(&plaintext, bytes.NewReader(stream), key); !errors.Is(err, format.ErrUnsupported) {
				t.Fatalf("DecryptStream error = %v, want ErrUnsupported", err)
			}
			if plaintext.Len() != 0 {
				t.Fatalf("DecryptStream wrote %d bytes before metadata rejection", plaintext.Len())
			}
		})
	}
}

func TestStreamRejectsShortHeaderAndBadMagic(t *testing.T) {
	key := testKey(9)
	encrypted := encryptToBytes(t, []byte("x"), key, testParams(t, 9))
	if _, err := DecryptStream(io.Discard, bytes.NewReader(encrypted[:format.HeaderLen-1]), key); !errors.Is(err, format.ErrShortHeader) {
		t.Fatalf("short header error = %v, want ErrShortHeader", err)
	}
	mutated := append([]byte(nil), encrypted...)
	mutated[0] ^= 0xff
	if _, err := DecryptStream(io.Discard, bytes.NewReader(mutated), key); !errors.Is(err, format.ErrBadMagic) {
		t.Fatalf("bad magic error = %v, want ErrBadMagic", err)
	}
}

func manualHKDFSHA256(ikm, salt []byte, info string) []byte {
	extract := hmac.New(sha256.New, salt)
	extract.Write(ikm)
	expand := hmac.New(sha256.New, extract.Sum(nil))
	expand.Write([]byte(info))
	expand.Write([]byte{1})
	return expand.Sum(nil)
}

func TestDeriveSubkeyUsesButterDBInfo(t *testing.T) {
	key := testKey(10)
	params := testParams(t, 10)
	got, err := DeriveSubkey(key, params.Salt)
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	if want := manualHKDFSHA256(key, params.Salt[:], "butterDB/file/v1"); !bytes.Equal(got, want) {
		t.Fatal("subkey does not match HKDF-SHA256 with info butterDB/file/v1")
	}
	if tdrive := manualHKDFSHA256(key, params.Salt[:], "tdrive/file/v1"); bytes.Equal(got, tdrive) {
		t.Fatal("subkey equals the TDrive derivation")
	}
	other := params.Salt
	other[0] ^= 1
	different, err := DeriveSubkey(key, other)
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	if bytes.Equal(got, different) {
		t.Fatal("different salts derived the same subkey")
	}
	if _, err := DeriveSubkey(key[:31], params.Salt); !errors.Is(err, ErrInvalidMasterKey) {
		t.Fatalf("short key error = %v, want ErrInvalidMasterKey", err)
	}
}

func TestNewFileParams(t *testing.T) {
	if _, err := NewFileParams(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil random error = %v, want ErrMissingDependency", err)
	}
	if _, err := NewFileParams(bytes.NewReader(make([]byte, format.SaltLen+format.NoncePrefixLen-1))); err == nil {
		t.Fatal("short random source accepted")
	}
	source := deterministicBytes(t, 11, format.SaltLen+format.NoncePrefixLen)
	params, err := NewFileParams(bytes.NewReader(source))
	if err != nil {
		t.Fatalf("NewFileParams: %v", err)
	}
	if !bytes.Equal(params.Salt[:], source[:format.SaltLen]) || !bytes.Equal(params.NoncePrefix[:], source[format.SaltLen:]) {
		t.Fatal("params do not follow the random source order")
	}
}

func TestPersistedSaltReproducesCiphertext(t *testing.T) {
	key := testKey(0x21)
	plain := deterministicBytes(t, 21, format.ChunkSize*2+123)
	persisted := testParams(t, 21)

	first := encryptToBytes(t, plain, key, persisted)
	resumed := encryptToBytes(t, plain, key, persisted)
	if !bytes.Equal(first, resumed) {
		t.Fatal("persisted salt and prefix did not reproduce identical ciphertext")
	}
	if int64(len(resumed)) != format.CiphertextSize(int64(len(plain))) {
		t.Fatalf("resumed stored size %d, want %d", len(resumed), format.CiphertextSize(int64(len(plain))))
	}

	otherSalt := persisted
	otherSalt.Salt[0] ^= 1
	if bytes.Equal(first, encryptToBytes(t, plain, key, otherSalt)) {
		t.Fatal("different salt produced identical ciphertext")
	}
	otherPrefix := persisted
	otherPrefix.NoncePrefix[0] ^= 1
	if bytes.Equal(first, encryptToBytes(t, plain, key, otherPrefix)) {
		t.Fatal("different nonce prefix produced identical ciphertext")
	}
}

func TestStreamGoldenVector(t *testing.T) {
	var params FileParams
	for i := range params.Salt {
		params.Salt[i] = byte(i)
	}
	for i := range params.NoncePrefix {
		params.NoncePrefix[i] = byte(0x80 + i)
	}
	encrypted := encryptToBytes(t, []byte("butterDB golden vector"), testKey(0x07), params)
	digest := sha256.Sum256(encrypted)
	if got := hex.EncodeToString(digest[:]); got != goldenVectorDigest {
		t.Fatalf("ciphertext digest = %s, want %s", got, goldenVectorDigest)
	}
}

const goldenVectorDigest = "1b0308c1ff1c72d0e745fbc44366f26afa50fbede81f491881a1a3b263693183"
