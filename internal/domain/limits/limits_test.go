package limits

import (
	"testing"
	"time"
)

func TestLimitsMatchSpec(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"max stored object bytes is 1900 MiB", MaxStoredObjectBytes, 1900 * 1024 * 1024},
		{"max stored object bytes literal", MaxStoredObjectBytes, 1992294400},
		{"max key bytes", MaxKeyBytes, 1024},
		{"max encoded key chars", MaxEncodedKeyChars, 3072},
		{"max user metadata bytes", MaxUserMetadataBytes, 2048},
		{"max system header bytes", MaxSystemHeaderBytes, 1024},
		{"max content type bytes", MaxContentTypeBytes, 1024},
		{"max put fixed chars", MaxPutFixedChars, 512},
		{"max put op chars", MaxPutOpChars, 4096},
		{"max caption chars", MaxCaptionChars, 1024},
		{"delete batch size", DeleteBatchSize, 100},
		{"max buckets", MaxBuckets, 100},
		{"max delete objects keys", MaxDeleteObjectsKeys, 1000},
		{"max multipart parts", MaxMultipartParts, 10000},
		{"min multipart part bytes is 5 MiB", MinMultipartPartBytes, 5 * 1024 * 1024},
		{"max cache max age seconds", MaxCacheMaxAgeSeconds, 31536000},
		{"max encrypted plaintext bytes", MaxEncryptedPlaintextBytes, 1991808062},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %d, want %d", tc.got, tc.want)
			}
		})
	}

	durations := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"orphan body grace", OrphanBodyGrace, 24 * time.Hour},
		{"idempotency ttl", IdempotencyTTL, 24 * time.Hour},
		{"default close timeout", DefaultCloseTimeout, 30 * time.Second},
	}
	for _, tc := range durations {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %s, want %s", tc.got, tc.want)
			}
		})
	}
}

func TestEncryptedCeilingArithmetic(t *testing.T) {
	const chunk = 65536
	stored := func(plain int64) int64 {
		return 50 + plain + 16*(plain/chunk+1)
	}
	cases := []struct {
		name  string
		plain int64
		want  int64
	}{
		{"empty plaintext", 0, 66},
		{"one chunk", 65536, 65618},
		{"largest plaintext fits exactly", MaxEncryptedPlaintextBytes, MaxStoredObjectBytes},
		{"one byte more is rejected", MaxEncryptedPlaintextBytes + 1, MaxStoredObjectBytes + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stored(tc.plain); got != tc.want {
				t.Fatalf("stored(%d) = %d, want %d", tc.plain, got, tc.want)
			}
		})
	}
}
