package wire

import (
	"strings"
	"testing"
)

func TestHeaderFirstLineOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"remainder with bad pairs ignored", bodyHeader + "\nnot a pair|=|\x00", nil},
		{"remainder with other op ignored", bodyHeader + "\nTDX1|t=snapshot", nil},
		{"carriage return before newline", bodyHeader + "\r\nmore", nil},
		{"surrounding spaces trimmed", "  " + bodyHeader + "  \nmore", nil},
		{"leading newline leaves empty header", "\n" + bodyHeader, ErrWireMissingHeader},
		{"header on second line is not read", "hello\n" + bodyHeader, ErrWireMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if err != tc.want {
				t.Fatalf("Parse error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExtractHeaderLineStripsBody(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"TDX1|t=body|j=x\nhuman comment", "TDX1|t=body|j=x"},
		{"TDX1|t=body|j=x", "TDX1|t=body|j=x"},
		{"  TDX1|t=body  \r\nrest", "  TDX1|t=body  \r"},
		{"\nTDX1", ""},
		{"", ""},
		{"a\nb\nc", "a"},
	}
	for _, tc := range cases {
		if got := ExtractHeaderLine(tc.raw); got != tc.want {
			t.Fatalf("ExtractHeaderLine(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestEmptyPairRejected(t *testing.T) {
	cases := []string{
		"TDX1|",
		"TDX1||t=body",
		"TDX1|t=body|",
		"TDX1|t=body||sz=1",
		bodyHeader + "|",
		bodyHeader + "|||",
		bodyHeader + "|noequals",
	}
	for _, raw := range cases {
		if _, err := Parse(raw); err != ErrWireMalformed {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMalformed", raw, err)
		}
	}
}

func TestEqualsAtIndexZeroRejected(t *testing.T) {
	cases := []string{
		"TDX1|=x|t=put",
		"TDX1|t=body|=1",
		bodyHeader + "|=",
		bodyHeader + "|=sz=1",
	}
	for _, raw := range cases {
		if _, err := Parse(raw); err != ErrWireMalformed {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMalformed", raw, err)
		}
	}
}

func TestValuesMayBeEmptyAtGrammarLevel(t *testing.T) {
	op, err := Parse("TDX1|t=zz|a=|b=")
	if err != ErrWireUnknownOp || op.Type != "zz" {
		t.Fatalf("Parse = %#v, %v; want unknown op zz", op, err)
	}
}

func TestEqualsInsideValueIsKept(t *testing.T) {
	op, err := Parse(headerWith(putHeader, "k", "a=b=c"))
	if err != nil || op.Key != "a=b=c" {
		t.Fatalf("Parse = %q, %v; want key a=b=c", op.Key, err)
	}
}

func TestHasPrefixClassification(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"hello", false},
		{"hello|t=put", false},
		{"", false},
		{"   ", false},
		{"\nTDX1|t=put", false},
		{"TDX1", true},
		{"TDX1x|t=put", true},
		{"TDX10|t=put", true},
		{"TDX1|t=zz", true},
		{"  TDX1|t=zz", true},
		{"TDX1|t=put\nrest", true},
		{"tdx1|t=put", false},
		{"TDX2|t=put", false},
	}
	for _, tc := range cases {
		if got := HasPrefix(tc.raw); got != tc.want {
			t.Fatalf("HasPrefix(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestIdentifierPatterns(t *testing.T) {
	cases := []struct {
		name  string
		check func(string) bool
		valid []string
		bad   []string
	}{
		{"job id", isJobID,
			[]string{"4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", "00000000-0000-4000-8000-000000000000", "ffffffff-ffff-4fff-bfff-ffffffffffff"},
			[]string{"", "4f9c2d1e8b3a4c7d9e2f1a6b5c4d3e2f", "4f9c2d1e-8b3a-5c7d-9e2f-1a6b5c4d3e2f", "4f9c2d1e-8b3a-4c7d-7e2f-1a6b5c4d3e2f", "4F9C2D1E-8B3A-4C7D-9E2F-1A6B5C4D3E2F", "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2", "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2fg"}},
		{"writer", isWriter,
			[]string{"key.A", "dev.k3m9x2q7w4t1z8r6", "sys.setup", "sys." + strings.Repeat("a", 32), "key.a_b-C"},
			[]string{"", "key", "key.", "usr.a", "key." + strings.Repeat("a", 33), "key.a.b", "key.a b", "KEY.a"}},
		{"bucket name", isBucketName,
			[]string{"abc", "photos", "a.b-c", strings.Repeat("a", 63)},
			[]string{"", "ab", strings.Repeat("a", 64), "Photos", "a_b", "a b"}},
		{"deployment id", isDeploymentID,
			[]string{"6pzyoxbfwcf6ubwcq5gpvjg5c4", strings.Repeat("a", 26), strings.Repeat("7", 26)},
			[]string{"", strings.Repeat("a", 25), strings.Repeat("a", 27), strings.Repeat("1", 26), strings.Repeat("A", 26), strings.Repeat("8", 26)}},
		{"kek fingerprint", isKEKFingerprint,
			[]string{"bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"},
			[]string{"", "bkf1_", "bkf2_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6", "3e9a0c51b7d24f6e8a01c9d2b3f4e5a6", "bkf1_3E9A0C51B7D24F6E8A01C9D2B3F4E5A6", "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a"}},
		{"etag", isETag,
			[]string{strings.Repeat("a", 32), strings.Repeat("a", 32) + "-1", strings.Repeat("a", 32) + "-10000", strings.Repeat("a", 32) + "-9999"},
			[]string{"", strings.Repeat("a", 32) + "-", strings.Repeat("a", 32) + "-0", strings.Repeat("a", 32) + "-10001", strings.Repeat("a", 32) + "-99999", strings.Repeat("a", 32) + "-05", strings.Repeat("a", 32) + "-+1", strings.Repeat("a", 31), strings.Repeat("g", 32)}},
		{"key id", isKeyID,
			[]string{"437ad4b129840534", strings.Repeat("0", 16)},
			[]string{"", strings.Repeat("0", 15), strings.Repeat("0", 17), strings.Repeat("A", 16)}},
		{"sha256", isSHA256,
			[]string{strings.Repeat("0", 64)},
			[]string{"", strings.Repeat("0", 63), strings.Repeat("0", 65), strings.Repeat("F", 64)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, s := range tc.valid {
				if !tc.check(s) {
					t.Fatalf("%q rejected, want accepted", s)
				}
			}
			for _, s := range tc.bad {
				if tc.check(s) {
					t.Fatalf("%q accepted, want rejected", s)
				}
			}
		})
	}
}

func TestIntegerSetters(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		low   int64
		high  int64
		valid bool
		want  int64
	}{
		{"in range", "5", 0, 10, true, 5},
		{"lower bound", "0", 0, 10, true, 0},
		{"upper bound", "10", 0, 10, true, 10},
		{"below range", "-1", 0, 10, false, 0},
		{"above range", "11", 0, 10, false, 0},
		{"empty", "", 0, 10, false, 0},
		{"leading plus", "+5", 0, 10, true, 5},
		{"leading zeros", "007", 0, 10, true, 7},
		{"underscore", "1_0", 0, 100, false, 0},
		{"hex", "0x5", 0, 10, false, 0},
		{"overflow", "99999999999999999999", 0, 10, false, 0},
		{"space", " 5", 0, 10, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int64
			err := setInt64Range(&got, tc.raw, tc.low, tc.high)
			if tc.valid && (err != nil || got != tc.want) {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
			if !tc.valid && err != ErrWireMalformed {
				t.Fatalf("error = %v, want ErrWireMalformed", err)
			}
		})
	}
}

func TestBase64Setter(t *testing.T) {
	var out []byte
	if err := setB64Len(&out, "AAAA", 3); err != nil || len(out) != 3 {
		t.Fatalf("valid base64: %v, %v", out, err)
	}
	for _, raw := range []string{"", "AAAA", "AAA=", "AA+A", "AA/A", "!!!!"} {
		if err := setB64Len(&out, raw, 2); err != ErrWireMalformed {
			t.Fatalf("setB64Len(%q) error = %v, want ErrWireMalformed", raw, err)
		}
	}
}
