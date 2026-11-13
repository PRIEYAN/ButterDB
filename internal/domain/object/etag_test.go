package object

import (
	"crypto/md5"
	"errors"
	"strings"
	"testing"
)

func TestETagFormat(t *testing.T) {
	emptySum := md5.Sum(nil)
	if got := SingleETag(emptySum); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("SingleETag(empty) = %q", got)
	}
	if got := QuoteETag(SingleETag(emptySum)); got != `"d41d8cd98f00b204e9800998ecf8427e"` {
		t.Fatalf("QuoteETag() = %q", got)
	}

	hex32 := strings.Repeat("a1", 16)
	tests := []struct {
		name    string
		etag    string
		wantErr bool
	}{
		{name: "single", etag: hex32},
		{name: "multipart", etag: hex32 + "-3"},
		{name: "multipart max parts", etag: hex32 + "-10000"},
		{name: "empty", etag: "", wantErr: true},
		{name: "too short", etag: hex32[:31], wantErr: true},
		{name: "too long", etag: hex32 + "0", wantErr: true},
		{name: "uppercase", etag: strings.ToUpper(hex32), wantErr: true},
		{name: "non hex", etag: strings.Repeat("g", 32), wantErr: true},
		{name: "quoted is not bare", etag: `"` + hex32 + `"`, wantErr: true},
		{name: "dash without count", etag: hex32 + "-", wantErr: true},
		{name: "zero parts", etag: hex32 + "-0", wantErr: true},
		{name: "leading zero count", etag: hex32 + "-01", wantErr: true},
		{name: "non digit count", etag: hex32 + "-x", wantErr: true},
		{name: "count over max", etag: hex32 + "-10001", wantErr: true},
		{name: "two dashes", etag: hex32 + "-1-2", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateETag(test.etag)
			if test.wantErr != (err != nil) {
				t.Fatalf("ValidateETag(%q) error = %v, wantErr %v", test.etag, err, test.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidETag) {
				t.Fatalf("ValidateETag(%q) error = %v, want ErrInvalidETag", test.etag, err)
			}
		})
	}

	quotedTests := []struct {
		name    string
		quoted  string
		want    string
		wantErr bool
	}{
		{name: "quoted single", quoted: `"` + hex32 + `"`, want: hex32},
		{name: "quoted multipart", quoted: `"` + hex32 + `-2"`, want: hex32 + "-2"},
		{name: "unquoted", quoted: hex32, wantErr: true},
		{name: "lone quote", quoted: `"`, wantErr: true},
		{name: "only open quote", quoted: `"` + hex32, wantErr: true},
		{name: "weak validator", quoted: `W/"` + hex32 + `"`, wantErr: true},
		{name: "quoted garbage", quoted: `"abc"`, wantErr: true},
	}
	for _, test := range quotedTests {
		t.Run("unquote "+test.name, func(t *testing.T) {
			got, err := UnquoteETag(test.quoted)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidETag) {
					t.Fatalf("UnquoteETag(%q) error = %v", test.quoted, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("UnquoteETag(%q) = %q, %v; want %q", test.quoted, got, err, test.want)
			}
		})
	}

	if IsMultipartETag(hex32) || !IsMultipartETag(hex32+"-2") || IsMultipartETag("nope-2") {
		t.Fatal("IsMultipartETag() classified a case wrongly")
	}
}

func TestMultipartETag(t *testing.T) {
	partA := md5.Sum([]byte("part one"))
	partB := md5.Sum([]byte("part two"))
	partC := md5.Sum([]byte("part three"))

	concatenated := func(sums ...[16]byte) []byte {
		var out []byte
		for _, sum := range sums {
			out = append(out, sum[:]...)
		}
		return out
	}
	digest := func(data []byte) string {
		sum := md5.Sum(data)
		return SingleETag(sum)
	}

	many := make([][16]byte, 10000)
	tooMany := make([][16]byte, 10001)

	tests := []struct {
		name    string
		parts   [][16]byte
		want    string
		wantErr error
	}{
		{name: "one part", parts: [][16]byte{partA}, want: digest(concatenated(partA)) + "-1"},
		{name: "two parts", parts: [][16]byte{partA, partB}, want: digest(concatenated(partA, partB)) + "-2"},
		{name: "order matters", parts: [][16]byte{partB, partA}, want: digest(concatenated(partB, partA)) + "-2"},
		{name: "three parts", parts: [][16]byte{partA, partB, partC}, want: digest(concatenated(partA, partB, partC)) + "-3"},
		{name: "ten thousand parts", parts: many, want: digest(make([]byte, 160000)) + "-10000"},
		{name: "no parts", parts: nil, wantErr: ErrNoParts},
		{name: "too many parts", parts: tooMany, wantErr: ErrTooManyParts},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MultipartETag(test.parts)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("MultipartETag() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("MultipartETag() = %q, want %q", got, test.want)
			}
			if err == nil {
				if verr := ValidateETag(got); verr != nil {
					t.Fatalf("MultipartETag() produced invalid etag %q: %v", got, verr)
				}
			}
		})
	}
}
