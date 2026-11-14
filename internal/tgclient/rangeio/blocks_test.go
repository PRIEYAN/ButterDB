package rangeio

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/gotd/td/tg"
)

func TestRoundedTelegramLimit(t *testing.T) {
	tests := []struct {
		name string
		want int
		got  int
	}{
		{name: "one byte", want: 1, got: int(RangeReadAlignment)},
		{name: "small", want: 128, got: int(RangeReadAlignment)},
		{name: "exact alignment", want: int(RangeReadAlignment), got: int(RangeReadAlignment)},
		{name: "one over alignment", want: int(RangeReadAlignment) + 1, got: int(RangeReadAlignment) * 2},
		{name: "one under max", want: RangeReadMaxBytes - 1, got: RangeReadMaxBytes},
		{name: "max", want: RangeReadMaxBytes, got: RangeReadMaxBytes},
		{name: "zero", want: 0, got: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := roundedTelegramLimit(test.want); got != test.got {
				t.Fatalf("roundedTelegramLimit(%d) = %d, want %d", test.want, got, test.got)
			}
		})
	}
}

func TestCrossesRangeBoundary(t *testing.T) {
	tests := []struct {
		name   string
		offset int64
		length int
		want   bool
	}{
		{name: "straddles the boundary", offset: int64(RangeReadMaxBytes) - 1, length: 2, want: true},
		{name: "ends at the boundary", offset: int64(RangeReadMaxBytes) - 2, length: 2, want: false},
		{name: "full aligned block", offset: int64(RangeReadMaxBytes), length: RangeReadMaxBytes, want: false},
		{name: "empty", offset: 5, length: 0, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := crossesRangeBoundary(test.offset, test.length); got != test.want {
				t.Fatalf("crossesRangeBoundary(%d, %d) = %v, want %v", test.offset, test.length, got, test.want)
			}
		})
	}
}

func TestCopyRequestedRange(t *testing.T) {
	dst := make([]byte, 3)
	n, err := copyRequestedRange(dst, []byte("abcdefgh"))
	if err != nil || n != 3 || !bytes.Equal(dst, []byte("abc")) {
		t.Fatalf("n=%d dst=%q err=%v", n, dst, err)
	}
	if _, err := copyRequestedRange(make([]byte, 4), []byte("ab")); err == nil {
		t.Fatal("a short source must fail")
	}
}

func hashFor(offset int64, limit int, data []byte) tg.FileHash {
	sum := sha256.Sum256(data)
	return tg.FileHash{Offset: offset, Limit: limit, Hash: sum[:]}
}

func TestVerifyFileHashes(t *testing.T) {
	data := []byte("abcdefghijklmnopqrstuvwxyz")
	h1 := hashFor(10, 8, data[:8])
	h2 := hashFor(18, 8, data[8:16])
	h3 := hashFor(26, len(data[16:]), data[16:])
	if err := verifyFileHashes(10, data, []tg.FileHash{h3, h1, h2}, nil); err != nil {
		t.Fatalf("verifyFileHashes: %v", err)
	}
}

func TestVerifyFileHashesRejectsCoverageGap(t *testing.T) {
	data := []byte("abcdefghijklmnop")
	err := verifyFileHashes(0, data, []tg.FileHash{hashFor(0, 8, data[:8]), hashFor(12, 4, data[12:])}, nil)
	if err == nil {
		t.Fatal("expected gap error")
	}
}

func TestVerifyFileHashesRejectsMismatch(t *testing.T) {
	data := []byte("abcdefgh")
	if err := verifyFileHashes(0, data, []tg.FileHash{hashFor(0, 8, []byte("xxxxxxxx"))}, nil); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestVerifyFileHashesRejectsMissingCoverage(t *testing.T) {
	data := []byte("abcdefgh")
	if err := verifyFileHashes(0, data, nil, nil); err == nil {
		t.Fatal("no hashes must not verify any bytes")
	}
}

func TestVerifyFileHashesFetchesTrailingPartialHashRange(t *testing.T) {
	full := []byte("abcdefgh")
	hash := hashFor(0, 8, full)
	calls := 0
	err := verifyFileHashes(0, full[:3], []tg.FileHash{hash}, func(got tg.FileHash) ([]byte, error) {
		calls++
		if got.Offset != hash.Offset || got.Limit != hash.Limit {
			t.Fatalf("fetch hash = %+v, want %+v", got, hash)
		}
		return full, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("verifyFileHashes = %v after %d fetches, want success after 1", err, calls)
	}
}

func TestVerifyFileHashesFetchesLeadingPartialHashRange(t *testing.T) {
	full := []byte("abcdefgh")
	calls := 0
	err := verifyFileHashes(4, full[4:], []tg.FileHash{hashFor(0, 8, full)}, func(tg.FileHash) ([]byte, error) {
		calls++
		return full, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("verifyFileHashes = %v after %d fetches, want success after 1", err, calls)
	}
}

func TestVerifyFileHashesRejectsPartialHashRangeWithoutFetch(t *testing.T) {
	full := []byte("abcdefgh")
	if err := verifyFileHashes(0, full[:3], []tg.FileHash{hashFor(0, 8, full)}, nil); err == nil {
		t.Fatal("expected partial hash range error")
	}
}
