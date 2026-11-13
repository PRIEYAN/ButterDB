package phase02

import (
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestDuplicatedConstantsMatchLimits(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"wire text chars and put op chars", wire.MaxTextChars, limits.MaxPutOpChars},
		{"wire caption chars", wire.MaxCaptionChars, limits.MaxCaptionChars},
		{"wire key bytes", wire.MaxKeyBytes, limits.MaxKeyBytes},
		{"wire encoded key chars", wire.MaxEncodedKeyChars, limits.MaxEncodedKeyChars},
		{"wire content type bytes", wire.MaxContentTypeBytes, limits.MaxContentTypeBytes},
		{"wire user metadata bytes", wire.MaxUserMetaBytes, limits.MaxUserMetadataBytes},
		{"wire system header bytes", wire.MaxSysHeaderBytes, limits.MaxSystemHeaderBytes},
		{"wire put fixed chars", wire.MaxPutFixedChars, limits.MaxPutFixedChars},
		{"wire stored bytes", wire.MaxStoredBytes, limits.MaxStoredObjectBytes},
		{"wire cache max age", wire.MaxCacheMaxAge, limits.MaxCacheMaxAgeSeconds},
		{"format encrypted plaintext bytes", format.MaxEncryptedPlaintextBytes, limits.MaxEncryptedPlaintextBytes},
		{"object key bytes", object.MaxKeyBytes, limits.MaxKeyBytes},
		{"object user metadata bytes", object.MaxUserMetadataBytes, limits.MaxUserMetadataBytes},
		{"bucket cache max age", bucket.MaxCacheMaxAgeSeconds, limits.MaxCacheMaxAgeSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %d, want %d", tc.got, tc.want)
			}
		})
	}
}

func TestStoredCapIsAgreedByWireAndFormat(t *testing.T) {
	if int64(limits.MaxStoredObjectBytes) != 1900*1024*1024 {
		t.Fatalf("limits cap = %d", int64(limits.MaxStoredObjectBytes))
	}
	if format.CiphertextSize(limits.MaxEncryptedPlaintextBytes) > wire.MaxStoredBytes {
		t.Fatal("largest encrypted plaintext exceeds the wire stored cap")
	}
	if format.CiphertextSize(limits.MaxEncryptedPlaintextBytes+1) <= wire.MaxStoredBytes {
		t.Fatal("format cap is not tight against the wire stored cap")
	}
}

func TestPutFixedPartBound(t *testing.T) {
	length, err := wire.PutRecordLen("k", "", nil, nil, "sys."+strings.Repeat("w", 32))
	if err != nil {
		t.Fatalf("PutRecordLen: %v", err)
	}
	fixed := length - len("k")
	if fixed != 286 {
		t.Fatalf("widest put fixed part = %d, want 286", fixed)
	}
	if fixed > limits.MaxPutFixedChars || fixed > wire.MaxPutFixedChars {
		t.Fatalf("fixed part %d exceeds the %d bound", fixed, limits.MaxPutFixedChars)
	}
}

func TestWorstCaseKeyAndMetadataFitOneTextMessage(t *testing.T) {
	key := strings.Repeat("a", wire.MaxKeyBytes)
	userMeta := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"} {
		userMeta[name] = strings.Repeat("v", 2048/16-1)
	}
	systemMeta := map[string]string{"cache-control": "max-age=1"}
	if err := wire.FitsPut(key, "image/jpeg", userMeta, systemMeta, "dev.k3m9x2q7w4t1z8r6"); err != nil {
		t.Fatalf("FitsPut: %v", err)
	}
	long := strings.Repeat("é", wire.MaxKeyBytes/2)
	if err := wire.FitsPut(long, "image/jpeg", userMeta, nil, "dev.k3m9x2q7w4t1z8r6"); err == nil {
		t.Fatal("a 3072-character key with full metadata fits one text message")
	}
}
