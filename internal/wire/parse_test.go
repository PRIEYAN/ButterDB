package wire

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func legacyOpTypes() []string {
	return []string{"f", "meta", "mkdir", "rename", "move", "rmdir", "tomb", "part", "manifest", "fcommit", "freplace", "dcommit", "relocate", "trash", "untrash", "harddel"}
}

func TestParseEmptyHeader(t *testing.T) {
	for _, raw := range []string{"", " ", "\t\r", "\nTDX1|t=body"} {
		if _, err := Parse(raw); !errors.Is(err, ErrWireMissingHeader) {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMissingHeader", raw, err)
		}
	}
}

func TestParseUnknownVersion(t *testing.T) {
	for _, raw := range []string{"TDX2|t=put|j=x", "TDX10|t=put", "TDX1x|t=put", "tdx1|t=put", "hello|t=put", "|t=put"} {
		if _, err := Parse(raw); !errors.Is(err, ErrWireBadVersion) {
			t.Fatalf("Parse(%q) error = %v, want ErrWireBadVersion", raw, err)
		}
	}
}

func TestParseUnknownOpType(t *testing.T) {
	op, err := Parse("TDX1|t=quux|obj=d:x|p=|n=foo")
	if !errors.Is(err, ErrWireUnknownOp) {
		t.Fatalf("error = %v, want ErrWireUnknownOp", err)
	}
	if op.Type != "quux" {
		t.Fatalf("type = %q, want quux", op.Type)
	}
}

func TestUnknownOpTypeIsUnknownOp(t *testing.T) {
	op, err := Parse("TDX1|t=snapshot|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|sz=-5|ts=-1")
	if !errors.Is(err, ErrWireUnknownOp) {
		t.Fatalf("error = %v, want ErrWireUnknownOp", err)
	}
	if want := (Op{Type: "snapshot"}); !reflect.DeepEqual(op, want) {
		t.Fatalf("op = %#v, want %#v", op, want)
	}
	if Known(op.Type) {
		t.Fatal("snapshot is known")
	}
}

func TestUnknownOpReturnsType(t *testing.T) {
	for _, name := range append(legacyOpTypes(), "snapshot") {
		t.Run(name, func(t *testing.T) {
			op, err := Parse("TDX1|t=" + name + "|obj=d:x|p=|n=x")
			if !errors.Is(err, ErrWireUnknownOp) {
				t.Fatalf("error = %v, want ErrWireUnknownOp", err)
			}
			if op.Type != OpType(name) {
				t.Fatalf("type = %q, want %q", op.Type, name)
			}
		})
	}
}

func TestLegacyOpsNotKnown(t *testing.T) {
	for _, name := range legacyOpTypes() {
		if Known(OpType(name)) {
			t.Fatalf("legacy op %q is known", name)
		}
		if formatterFor(OpType(name)) != nil {
			t.Fatalf("legacy op %q has a formatter", name)
		}
		if _, err := Format(Op{Type: OpType(name)}); !errors.Is(err, ErrWireUnknownOp) {
			t.Fatalf("Format(%q) error = %v, want ErrWireUnknownOp", name, err)
		}
	}
	for _, known := range []OpType{OpBody, OpPut, OpDel, OpRegistry, OpBucketCreate, OpBucketConfig, OpBucketDelete, OpEncConfig} {
		if !Known(known) {
			t.Fatalf("%q is not known", known)
		}
	}
	for _, unknown := range []OpType{"", "PUT", "Body", " put", "put "} {
		if Known(unknown) {
			t.Fatalf("%q is known", unknown)
		}
	}
}

func TestOnDocument(t *testing.T) {
	for _, opType := range []OpType{OpBody, OpPut, OpDel, OpRegistry, OpBucketCreate, OpBucketConfig, OpBucketDelete, OpEncConfig, "zz"} {
		if got, want := OnDocument(opType), opType == OpBody; got != want {
			t.Fatalf("OnDocument(%q) = %v, want %v", opType, got, want)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   error
		wantOp OpType
	}{
		{"bad version", "TDX2|t=put|j=x", ErrWireBadVersion, ""},
		{"one part", "TDX1", ErrWireMalformed, ""},
		{"equals at index zero", "TDX1|=x|t=put", ErrWireMalformed, ""},
		{"empty key", headerWith(putHeader, "k", ""), ErrWireMalformed, ""},
		{"negative size", headerWith(putHeader, "sz", "-1"), ErrWireMalformed, ""},
		{"plaintext size without enc", headerWith(putHeader, "psz", "10"), ErrWireMalformed, ""},
		{"enc without key id", headerWithout(encPutHeader, "kid"), ErrWireMalformed, ""},
		{"empty object with size", headerWith(emptyPutHeader, "sz", "5"), ErrWireMalformed, ""},
		{"wrapped key of 71 bytes", headerWith(encfgHeader, "wrap", strings.Repeat("A", 95)), ErrWireMalformed, ""},
		{"min reader above format version", headerWith(regHeader, "mr", "2"), ErrWireMalformed, ""},
		{"snapshot op", "TDX1|t=snapshot|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", ErrWireUnknownOp, "snapshot"},
		{"legacy mkdir op", "TDX1|t=mkdir|obj=d:7d3f|p=|n=x", ErrWireUnknownOp, "mkdir"},
		{"plain text", "hello", ErrWireMalformed, ""},
		{"plain text with pair", "hello|t=put", ErrWireBadVersion, ""},
		{"empty type", "TDX1|t=|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", ErrWireMalformed, ""},
		{"empty type alone", "TDX1|t=", ErrWireMalformed, ""},
		{"missing type", "TDX1|j=4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f", ErrWireMalformed, ""},
		{"header over the text limit", "TDX1|t=put|k=" + strings.Repeat("a", MaxTextChars), ErrWireTooLong, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := Parse(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if op.Type != tc.wantOp {
				t.Fatalf("type = %q, want %q", op.Type, tc.wantOp)
			}
		})
	}
}

func TestUnknownKeysIgnored(t *testing.T) {
	for i, header := range workedExampleHeaders(t) {
		want, err := Parse(header)
		if err != nil {
			t.Fatalf("example %d: %v", i, err)
		}
		for _, extra := range []string{"zz=1", "zz=", "future=%FF", "obj=d:x", "m=1", "x.y=z"} {
			got, err := Parse(header + "|" + extra)
			if err != nil {
				t.Fatalf("example %d with %q: %v", i, extra, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("example %d with %q:\n got: %#v\nwant: %#v", i, extra, got, want)
			}
		}
	}
}

func TestRepeatedKeyLastWins(t *testing.T) {
	op, err := Parse(putHeader + "|k=a|k=b")
	if err != nil || op.Key != "b" {
		t.Fatalf("Parse = %q, %v; want key b", op.Key, err)
	}
	op, err = Parse(bodyHeader + "|sz=7|sz=9")
	if err != nil || op.StoredSize != 9 {
		t.Fatalf("Parse = %d, %v; want size 9", op.StoredSize, err)
	}
	if _, err := Parse(bodyHeader + "|sz=7|sz=-1"); !errors.Is(err, ErrWireMalformed) {
		t.Fatalf("last malformed value error = %v, want ErrWireMalformed", err)
	}
	if _, err := Parse("TDX1|t=zz|t=body|j=" + "4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f|sz=1"); err != nil {
		t.Fatalf("repeated t: %v", err)
	}
}

func TestRemainderIgnored(t *testing.T) {
	for i, header := range workedExampleHeaders(t) {
		want, err := Parse(header)
		if err != nil {
			t.Fatal(err)
		}
		for _, remainder := range []string{"\n", "\nhello", "\n\n\n", "\nTDX1|t=zz", "\r\nk=a|k=b", "\n\x00\xff"} {
			got, err := Parse(header + remainder)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("example %d with remainder %q: %#v, %v", i, remainder, got, err)
			}
		}
	}
}

func TestSharedAttrsOnEveryType(t *testing.T) {
	for i, header := range workedExampleHeaders(t) {
		for _, bad := range []string{"sz=-1", "sz=x", "ts=-1", "ts=253402300800", "ts=x", "ev=x", "psz=-1", "psz=x"} {
			if _, err := Parse(header + "|" + bad); !errors.Is(err, ErrWireMalformed) {
				t.Fatalf("example %d with %q error = %v, want ErrWireMalformed", i, bad, err)
			}
		}
	}
	if _, err := Parse(headerWith(regHeader, "sz", "-1")); !errors.Is(err, ErrWireMalformed) {
		t.Fatalf("reg with sz=-1 error = %v, want ErrWireMalformed", err)
	}
}

func TestSharedAttrsIgnoredWhereUnused(t *testing.T) {
	want, err := Parse(regHeader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(regHeader + "|sz=5|psz=6|enc=1|ev=2")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reg with unused shared keys: %#v, %v", got, err)
	}
	body, err := Parse(bodyHeader + "|ts=5|psz=6")
	if err != nil || body.Timestamp != 0 || body.PlaintextSize != 0 {
		t.Fatalf("body with unused shared keys: %#v, %v", body, err)
	}
}

func TestTimestampBounds(t *testing.T) {
	cases := []struct {
		ts    string
		valid bool
	}{
		{"0", true},
		{"1", true},
		{"253402300799", true},
		{"253402300800", false},
		{"-1", false},
		{"9223372036854775807", false},
		{"9223372036854775808", false},
		{"", false},
	}
	for _, tc := range cases {
		for _, header := range []string{putHeader, delHeader, regHeader, bcreateHeader, bdelHeader, encfgHeader} {
			_, err := Parse(headerWith(header, "ts", tc.ts))
			if tc.valid && err != nil {
				t.Fatalf("ts=%s on %.20s: %v", tc.ts, header, err)
			}
			if !tc.valid && !errors.Is(err, ErrWireMalformed) {
				t.Fatalf("ts=%s on %.20s error = %v, want ErrWireMalformed", tc.ts, header, err)
			}
		}
	}
}

func TestParseRejectsUploadTimeOutsideSQLiteRange(t *testing.T) {
	if _, err := Parse(headerWith(putHeader, "ts", "253402300800")); !errors.Is(err, ErrWireMalformed) {
		t.Fatalf("error = %v, want ErrWireMalformed", err)
	}
}

func TestParseRejectsNegativeFileSizes(t *testing.T) {
	for _, raw := range []string{
		headerWith(putHeader, "sz", "-1"),
		headerWith(putHeader, "ts", "-1"),
		headerWith(encPutHeader, "psz", "-1"),
		headerWith(bodyHeader, "sz", "-1"),
	} {
		if _, err := Parse(raw); !errors.Is(err, ErrWireMalformed) {
			t.Fatalf("Parse(%.60q) error = %v, want ErrWireMalformed", raw, err)
		}
	}
}

func TestParseNormalisation(t *testing.T) {
	op, err := Parse(putHeader)
	if err != nil {
		t.Fatal(err)
	}
	if op.PlaintextSize != op.StoredSize || op.Encrypted || op.EncryptionVersion != 0 || op.KeyID != "" {
		t.Fatalf("unencrypted normalisation: %#v", op)
	}
	if op.SystemMeta != nil {
		t.Fatalf("system meta = %#v, want nil", op.SystemMeta)
	}
	op, err = Parse(headerWithout(putHeader, "ct"))
	if err != nil || op.ContentType != DefaultContentType || len(op.UserMeta) != 2 {
		t.Fatalf("default content type: %#v, %v", op, err)
	}
	op, err = Parse(encPutHeader)
	if err != nil || !op.Encrypted || op.EncryptionVersion != 1 || op.UserMeta != nil {
		t.Fatalf("encrypted normalisation: %#v, %v", op, err)
	}
	op, err = Parse(encfgHeader)
	if err != nil || op.ConfigVersion != 1 {
		t.Fatalf("encfg config version: %#v, %v", op, err)
	}
	op, err = Parse(headerWith(encPutHeader, "ev", "2"))
	if err != nil || op.EncryptionVersion != 2 {
		t.Fatalf("encryption version override: %#v, %v", op, err)
	}
}

func TestParseTolerantIntegerForms(t *testing.T) {
	op, err := Parse(headerWith(bodyHeader, "sz", "+0482113"))
	if err != nil || op.StoredSize != 482113 {
		t.Fatalf("Parse = %d, %v; want 482113", op.StoredSize, err)
	}
}

func FuzzParseNoPanic(f *testing.F) {
	for _, header := range workedExampleHeaders(f) {
		f.Add(header)
	}
	for _, seed := range []string{"", "TDX1", "TDX1|", "TDX1|=", "TDX1|t=", "TDX1|t=put|k=%", "hello\nTDX1|t=put", "TDX1|t=put|m.=|s.=", "\x00|\xff=\xfe"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		op, err := Parse(raw)
		switch {
		case err == nil:
			if !Known(op.Type) {
				t.Fatalf("Parse succeeded with unknown type %q", op.Type)
			}
		case errors.Is(err, ErrWireUnknownOp):
			if Known(op.Type) {
				t.Fatalf("unknown op error with known type %q", op.Type)
			}
		default:
			if !reflect.DeepEqual(op, Op{}) {
				t.Fatalf("error %v with populated op %#v", err, op)
			}
		}
		HasPrefix(raw)
		ExtractHeaderLine(raw)
	})
}
