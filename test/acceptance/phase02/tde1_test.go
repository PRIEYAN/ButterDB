package phase02

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

func TestTDE1SizeVectorsMatch(t *testing.T) {
	vectors := []struct {
		name      string
		plaintext int64
		stored    int64
	}{
		{"empty file is header plus one tag", 0, 66},
		{"one byte", 1, 67},
		{"one byte below a chunk", 65535, 65601},
		{"exactly one chunk gets a second final chunk", 65536, 65618},
		{"one byte past a chunk", 65537, 65619},
		{"two full chunks", 131072, 131170},
		{"734003 bytes from the worked example", 734003, 734245},
		{"largest encrypted plaintext fits the stored cap exactly", limits.MaxEncryptedPlaintextBytes, limits.MaxStoredObjectBytes},
	}
	for _, tc := range vectors {
		t.Run(tc.name, func(t *testing.T) {
			if got := format.CiphertextSize(tc.plaintext); got != tc.stored {
				t.Fatalf("CiphertextSize(%d) = %d, want %d", tc.plaintext, got, tc.stored)
			}
		})
	}

	t.Run("header is 50 bytes", func(t *testing.T) {
		if format.HeaderLen != 50 || format.ChunkSize != 65536 || format.TagLen != 16 || format.SaltLen != 16 || format.NoncePrefixLen != 20 {
			t.Fatalf("layout = %d %d %d %d %d", format.HeaderLen, format.ChunkSize, format.TagLen, format.SaltLen, format.NoncePrefixLen)
		}
	})

	t.Run("largest plaintext fits and one more does not", func(t *testing.T) {
		if got := format.CiphertextSize(format.MaxEncryptedPlaintextBytes); got > limits.MaxStoredObjectBytes {
			t.Fatalf("stored size %d exceeds %d", got, int64(limits.MaxStoredObjectBytes))
		}
		if got := format.CiphertextSize(format.MaxEncryptedPlaintextBytes + 1); got <= limits.MaxStoredObjectBytes {
			t.Fatalf("one more byte still fits: %d", got)
		}
	})

	t.Run("saturates at max int64", func(t *testing.T) {
		if got := format.CiphertextSize(math.MaxInt64); got != math.MaxInt64 {
			t.Fatalf("CiphertextSize(MaxInt64) = %d", got)
		}
	})

	t.Run("plaintext size ceiling is enforced", func(t *testing.T) {
		if err := format.ValidatePlaintextSize(format.MaxPlaintextSize); err != nil {
			t.Fatalf("ceiling rejected: %v", err)
		}
		if err := format.ValidatePlaintextSize(format.MaxPlaintextSize + 1); !errors.Is(err, format.ErrPlaintextTooLarge) {
			t.Fatalf("past the ceiling = %v", err)
		}
		if err := format.ValidatePlaintextSize(-1); !errors.Is(err, format.ErrNegativePlaintextSize) {
			t.Fatalf("negative = %v", err)
		}
	})

	t.Run("stored offsets skip the header and earlier tags", func(t *testing.T) {
		offsets := []struct{ plaintext, stored int64 }{
			{0, 50},
			{65535, 50 + 65535},
			{65536, 50 + 65536 + 16},
			{131072, 50 + 2*(65536+16)},
		}
		for _, o := range offsets {
			got, ok := format.StoredOffset(o.plaintext)
			if !ok || got != o.stored {
				t.Fatalf("StoredOffset(%d) = %d, %v; want %d", o.plaintext, got, ok, o.stored)
			}
		}
	})

	t.Run("header round trips and the nonce marks the final chunk", func(t *testing.T) {
		header := format.Header{PlaintextSize: 734003}
		for i := range header.Salt {
			header.Salt[i] = byte(i + 1)
		}
		for i := range header.NoncePrefix {
			header.NoncePrefix[i] = byte(0x40 + i)
		}
		encoded, err := format.EncodeHeader(header)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !bytes.HasPrefix(encoded[:], []byte(format.Magic)) || len(encoded) != format.HeaderLen {
			t.Fatalf("encoded header = %x", encoded)
		}
		decoded, err := format.DecodeHeader(encoded[:])
		if err != nil || decoded != header {
			t.Fatalf("decode = %+v, %v", decoded, err)
		}
		plain := format.BuildNonce(header.NoncePrefix, 7, false)
		final := format.BuildNonce(header.NoncePrefix, 7, true)
		if plain[format.NoncePrefixLen+3] != 7 || final[format.NoncePrefixLen] != 0x80 {
			t.Fatalf("nonce counters = %x / %x", plain, final)
		}
	})
}
