package wire

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTripFormatParse(t *testing.T) {
	for _, op := range workedExampleOps(t) {
		raw, err := Format(op)
		if err != nil {
			t.Fatalf("Format(%s): %v", op.Type, err)
		}
		if !strings.HasPrefix(raw, "TDX1|t="+string(op.Type)+"|") {
			t.Fatalf("Format(%s) = %q, missing prefix", op.Type, raw)
		}
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if !reflect.DeepEqual(got, op) {
			t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, op)
		}
	}
}

func TestEscapingRoundTrip(t *testing.T) {
	var allPrintable strings.Builder
	for c := 0x20; c <= 0x7e; c++ {
		allPrintable.WriteByte(byte(c))
	}
	op := workedExampleOps(t)[5]
	op.Key = allPrintable.String() + "\t\n\u00e9\U0001F600"
	op.ContentType = allPrintable.String()
	op.UserMeta = map[string]string{
		"a!#$%&'*+.^_`|~-": allPrintable.String() + "\t\u00e9\U0001F600",
		"plain":            "",
	}
	op.SystemMeta = map[string]string{"content-disposition": "attachment; filename=\"a b|c=d%e+f.txt\""}
	header, err := Format(op)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	got, err := Parse(header)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got, op) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, op)
	}
}

func TestHeaderIsASCII(t *testing.T) {
	ops := workedExampleOps(t)
	for _, entry := range roundTripTable(t) {
		ops = append(ops, entry.op)
	}
	hostile := ops[5]
	hostile.Key = " \n|=%+\u00e9\U0001F600 "
	hostile.UserMeta = map[string]string{"k": "caf\u00e9"}
	ops = append(ops, hostile)
	for _, op := range ops {
		header, err := Format(op)
		if err != nil {
			t.Fatalf("Format(%s): %v", op.Type, err)
		}
		for i := 0; i < len(header); i++ {
			if header[i] >= 0x80 || header[i] < 0x20 || header[i] == 0x7f {
				t.Fatalf("header %q has byte %#x", header, header[i])
			}
		}
		if header != strings.TrimSpace(header) || strings.Contains(header, "\n") {
			t.Fatalf("header %q has surrounding whitespace or a newline", header)
		}
	}
}

func TestFormatRejectsUnknownOp(t *testing.T) {
	for _, opType := range []OpType{"", "snapshot", "mkdir", "PUT"} {
		if _, err := Format(Op{Type: opType}); !errors.Is(err, ErrWireUnknownOp) {
			t.Fatalf("Format(%q) error = %v, want ErrWireUnknownOp", opType, err)
		}
	}
}

func TestFormatRejectsInvalidFields(t *testing.T) {
	examples := workedExampleOps(t)
	cases := []struct {
		name string
		op   Op
		mod  func(*Op)
	}{
		{"put empty content type", examples[5], func(op *Op) { op.ContentType = "" }},
		{"put control byte in content type", examples[5], func(op *Op) { op.ContentType = "a\x01" }},
		{"put uppercase meta name", examples[5], func(op *Op) { op.UserMeta = map[string]string{"A": "x"} }},
		{"put meta value with newline", examples[5], func(op *Op) { op.UserMeta = map[string]string{"a": "x\ny"} }},
		{"put meta value invalid utf8", examples[5], func(op *Op) { op.UserMeta = map[string]string{"a": "\xff"} }},
		{"put unknown system header", examples[5], func(op *Op) { op.SystemMeta = map[string]string{"etag": "x"} }},
		{"put empty system header", examples[5], func(op *Op) { op.SystemMeta = map[string]string{"expires": ""} }},
		{"put key id without encryption", examples[5], func(op *Op) { op.KeyID = "437ad4b129840534" }},
		{"put plaintext size mismatch", examples[5], func(op *Op) { op.PlaintextSize = 7 }},
		{"put encrypted without key id", examples[7], func(op *Op) { op.KeyID = "" }},
		{"put encrypted version zero", examples[7], func(op *Op) { op.EncryptionVersion = 0 }},
		{"put negative plaintext size", examples[7], func(op *Op) { op.PlaintextSize = -1 }},
		{"put timestamp above bound", examples[5], func(op *Op) { op.Timestamp = MaxUnixTimestamp + 1 }},
		{"put negative timestamp", examples[5], func(op *Op) { op.Timestamp = -1 }},
		{"put body message id above bound", examples[5], func(op *Op) { op.BodyMsgID = MaxMsgID + 1 }},
		{"put size above stored maximum", examples[5], func(op *Op) { op.StoredSize = MaxStoredBytes + 1 }},
		{"put empty object with body", examples[9], func(op *Op) { op.StoredSize = 1 }},
		{"put empty writer", examples[5], func(op *Op) { op.Writer = "" }},
		{"body zero size", examples[4], func(op *Op) { op.StoredSize = 0 }},
		{"body encrypted version zero", examples[6], func(op *Op) { op.EncryptionVersion = 0 }},
		{"del negative put version", examples[10], func(op *Op) { op.PutVersion = -1 }},
		{"del empty writer", examples[10], func(op *Op) { op.Writer = "" }},
		{"reg min reader above format version", examples[0], func(op *Op) { op.MinReaderVersion = 2 }},
		{"reg zero user id", examples[0], func(op *Op) { op.PrimaryUserID = 0 }},
		{"reg bad writer", examples[0], func(op *Op) { op.Writer = "setup" }},
		{"bcreate bad policy", examples[1], func(op *Op) { op.Policy = "" }},
		{"bcreate max age above limit", examples[1], func(op *Op) { op.CacheMaxAge = MaxCacheMaxAge + 1 }},
		{"bcfg zero channel", examples[3], func(op *Op) { op.BucketChannelID = 0 }},
		{"bdel short bucket", examples[11], func(op *Op) { op.Bucket = "ab" }},
		{"encfg short wrapped key", examples[2], func(op *Op) { op.WrappedKey = op.WrappedKey[:71] }},
		{"encfg short key check", examples[2], func(op *Op) { op.KeyCheck = nil }},
		{"encfg negative config version", examples[2], func(op *Op) { op.ConfigVersion = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.op
			op.WrappedKey = append([]byte(nil), tc.op.WrappedKey...)
			tc.mod(&op)
			if _, err := Format(op); !errors.Is(err, ErrWireMalformed) {
				t.Fatalf("Format error = %v, want ErrWireMalformed", err)
			}
		})
	}
}

func TestFormatIsLenientAboutUnsetNormalisedFields(t *testing.T) {
	op := workedExampleOps(t)[5]
	op.PlaintextSize = 0
	header, err := Format(op)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	got, err := Parse(header)
	if err != nil || got.PlaintextSize != op.StoredSize {
		t.Fatalf("Parse = %d, %v; want plaintext size %d", got.PlaintextSize, err, op.StoredSize)
	}
	config := workedExampleOps(t)[2]
	config.ConfigVersion = 0
	header, err = Format(config)
	if err != nil || strings.Contains(header, "|cv=") {
		t.Fatalf("Format = %q, %v; want no cv key", header, err)
	}
}

func TestFormatEmitsEachKeyOnce(t *testing.T) {
	for _, entry := range roundTripTable(t) {
		header, err := Format(entry.op)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, part := range strings.Split(header, "|")[1:] {
			key, _, _ := strings.Cut(part, "=")
			if seen[key] {
				t.Fatalf("%s: key %q emitted twice in %q", entry.name, key, header)
			}
			seen[key] = true
		}
	}
}

func TestCaptionLimitBody(t *testing.T) {
	if carrierLimit(OpBody) != MaxCaptionChars || carrierLimit(OpPut) != MaxTextChars {
		t.Fatalf("carrier limits: body %d, put %d", carrierLimit(OpBody), carrierLimit(OpPut))
	}
	widest := Op{Type: OpBody, JobID: widestJobID, StoredSize: MaxStoredBytes, Encrypted: true, EncryptionVersion: 2147483647}
	header, err := Format(widest)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if len(header) != 84 {
		t.Fatalf("widest body is %d characters (%q)", len(header), header)
	}
	widest.EncryptionVersion = 1
	if header, err = Format(widest); err != nil || len(header) != 70 {
		t.Fatalf("body without ev is %d characters, %v; want 70", len(header), err)
	}
	if len(header) > MaxCaptionChars {
		t.Fatalf("widest body exceeds the caption limit: %d", len(header))
	}
}
