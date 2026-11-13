package format

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestCiphertextSizeVectors(t *testing.T) {
	cases := []struct {
		name  string
		plain int64
		want  int64
	}{
		{"empty", 0, 66},
		{"one byte", 1, 67},
		{"one short of chunk", 65535, 65601},
		{"exact chunk", 65536, 65618},
		{"one over chunk", 65537, 65619},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CiphertextSize(tc.plain); got != tc.want {
				t.Fatalf("CiphertextSize(%d) = %d, want %d", tc.plain, got, tc.want)
			}
		})
	}
}

func TestCiphertextSizeSaturatesOnOverflow(t *testing.T) {
	if got := CiphertextSize(math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("CiphertextSize(MaxInt64) = %d, want MaxInt64", got)
	}
	if got := CiphertextSize(MaxPlaintextSize + 1); got != math.MaxInt64 {
		t.Fatalf("CiphertextSize(above capacity) = %d, want MaxInt64", got)
	}
}

func TestValidatePlaintextSizeEnforcesTDE1CounterCapacity(t *testing.T) {
	if finalIndex := MaxPlaintextSize / int64(ChunkSize); finalIndex >= int64(FinalChunkBit) {
		t.Fatalf("maximum plaintext final chunk index = %d, must keep the final-marker bit clear", finalIndex)
	}
	tests := []struct {
		name string
		size int64
		want error
	}{
		{name: "negative", size: -1, want: ErrNegativePlaintextSize},
		{name: "empty", size: 0},
		{name: "maximum", size: MaxPlaintextSize},
		{name: "above maximum", size: MaxPlaintextSize + 1, want: ErrPlaintextTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlaintextSize(tt.size)
			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("ValidatePlaintextSize(%d) = %v, want %v", tt.size, err, tt.want)
			}
		})
	}
}

func TestStoredOffsetSkipsHeaderAndEarlierChunkTags(t *testing.T) {
	cases := map[int64]int64{
		0:               HeaderLen,
		ChunkSize - 1:   HeaderLen + ChunkSize - 1,
		ChunkSize:       HeaderLen + ChunkSize + TagLen,
		3*ChunkSize + 7: HeaderLen + 3*(ChunkSize+TagLen) + 7,
	}
	for plain, want := range cases {
		got, ok := StoredOffset(plain)
		if !ok || got != want {
			t.Fatalf("StoredOffset(%d) = %d, %t; want %d", plain, got, ok, want)
		}
	}
	if _, ok := StoredOffset(-1); ok {
		t.Fatal("negative offsets have no stored position")
	}
	if _, ok := StoredOffset(math.MaxInt64); ok {
		t.Fatal("offset past int64 stored range must be rejected")
	}
	if _, ok := ChunkStoredOffset(-1); ok {
		t.Fatal("negative chunk index has no stored position")
	}
}

func TestMaxEncryptedPlaintextFits(t *testing.T) {
	const want = 1991808062
	const limit = 1900 * 1024 * 1024
	if MaxEncryptedPlaintextBytes != want {
		t.Fatalf("MaxEncryptedPlaintextBytes = %d, want %d", int64(MaxEncryptedPlaintextBytes), want)
	}
	if got := CiphertextSize(MaxEncryptedPlaintextBytes); got > limit {
		t.Fatalf("CiphertextSize(max) = %d exceeds %d", got, limit)
	}
	if got := CiphertextSize(MaxEncryptedPlaintextBytes); got != limit {
		t.Fatalf("CiphertextSize(max) = %d, want %d", got, limit)
	}
	if got := CiphertextSize(MaxEncryptedPlaintextBytes + 1); got <= limit {
		t.Fatalf("CiphertextSize(max+1) = %d must exceed %d", got, limit)
	}
}

func testHeader() Header {
	var h Header
	for i := range h.Salt {
		h.Salt[i] = byte(0x10 + i)
	}
	for i := range h.NoncePrefix {
		h.NoncePrefix[i] = byte(0x40 + i)
	}
	h.PlaintextSize = 0x0102030405
	return h
}

func TestHeaderRoundTrip(t *testing.T) {
	sizes := []int64{0, 1, ChunkSize, 0x0102030405, MaxPlaintextSize}
	for _, size := range sizes {
		h := testHeader()
		h.PlaintextSize = size
		enc, err := EncodeHeader(h)
		if err != nil {
			t.Fatalf("EncodeHeader(%d): %v", size, err)
		}
		got, err := DecodeHeader(enc[:])
		if err != nil {
			t.Fatalf("DecodeHeader(%d): %v", size, err)
		}
		if got != h {
			t.Fatalf("round trip = %+v, want %+v", got, h)
		}
	}

	enc, err := EncodeHeader(testHeader())
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("TDE1\x00\x10")
	fixture := testHeader()
	want = append(want, fixture.Salt[:]...)
	want = append(want, fixture.NoncePrefix[:]...)
	want = append(want, 0x05, 0x04, 0x03, 0x02, 0x01, 0, 0, 0)
	if !bytes.Equal(enc[:], want) {
		t.Fatalf("encoded header = %x, want %x", enc, want)
	}
	if _, err := EncodeHeader(Header{PlaintextSize: -1}); !errors.Is(err, ErrNegativePlaintextSize) {
		t.Fatalf("negative size error = %v", err)
	}
	if _, err := EncodeHeader(Header{PlaintextSize: MaxPlaintextSize + 1}); !errors.Is(err, ErrPlaintextTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestHeaderRejectsBadMagicFlagsLog2(t *testing.T) {
	good, err := EncodeHeader(testHeader())
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good[:]...)
		f(b)
		return b
	}
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShortHeader},
		{"one byte short", good[:HeaderLen-1], ErrShortHeader},
		{"bad magic", mutate(func(b []byte) { b[3] = '2' }), ErrBadMagic},
		{"nonzero flags", mutate(func(b []byte) { b[4] = 1 }), ErrUnsupported},
		{"log2 15", mutate(func(b []byte) { b[5] = 15 }), ErrUnsupported},
		{"log2 17", mutate(func(b []byte) { b[5] = 17 }), ErrUnsupported},
		{"size above capacity", mutate(func(b []byte) {
			b[42], b[43], b[44], b[45], b[46], b[47], b[48], b[49] = 0, 0, 0, 0, 0, 0x80, 0, 0
		}), ErrPlaintextTooLarge},
		{"size high bit", mutate(func(b []byte) { b[49] = 0x80 }), ErrPlaintextTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeHeader(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("DecodeHeader error = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := DecodeHeader(append(append([]byte(nil), good[:]...), 0xAA)); err != nil {
		t.Fatalf("trailing bytes after header must be accepted: %v", err)
	}
}

func TestNonceFinalBit(t *testing.T) {
	var prefix [NoncePrefixLen]byte
	for i := range prefix {
		prefix[i] = byte(i + 1)
	}
	tests := []struct {
		name    string
		counter uint32
		final   bool
		tail    []byte
	}{
		{"first chunk", 0, false, []byte{0, 0, 0, 0}},
		{"counter 5", 5, false, []byte{0, 0, 0, 5}},
		{"counter 0x01020304", 0x01020304, false, []byte{1, 2, 3, 4}},
		{"final zero", 0, true, []byte{0x80, 0, 0, 0}},
		{"final counter 5", 5, true, []byte{0x80, 0, 0, 5}},
		{"largest raw counter", FinalChunkBit - 1, false, []byte{0x7f, 0xff, 0xff, 0xff}},
		{"largest raw counter final", FinalChunkBit - 1, true, []byte{0xff, 0xff, 0xff, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildNonce(prefix, tt.counter, tt.final)
			if len(got) != 24 {
				t.Fatalf("nonce length = %d, want 24", len(got))
			}
			if !bytes.Equal(got[:20], prefix[:]) {
				t.Fatalf("nonce prefix = %x, want %x", got[:20], prefix)
			}
			if !bytes.Equal(got[20:], tt.tail) {
				t.Fatalf("nonce counter = %x, want %x", got[20:], tt.tail)
			}
		})
	}
	if BuildNonce(prefix, 7, false) == BuildNonce(prefix, 7, true) {
		t.Fatal("final and non-final nonces must differ")
	}
}
