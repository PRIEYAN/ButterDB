package wire

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func roundTripTable(t testing.TB) []struct {
	name string
	op   Op
} {
	t.Helper()
	examples := workedExampleOps(t)
	basePut := examples[5]
	baseEncPut := examples[7]
	baseBody := examples[4]
	baseDel := examples[10]
	baseReg := examples[0]
	baseCreate := examples[1]
	baseConfig := examples[3]
	baseDelete := examples[11]
	baseEncfg := examples[2]

	with := func(base Op, change func(*Op)) Op {
		op := base
		op.UserMeta = copyMap(base.UserMeta)
		op.SystemMeta = copyMap(base.SystemMeta)
		op.WrappedKey = bytes.Clone(base.WrappedKey)
		op.KeyCheck = bytes.Clone(base.KeyCheck)
		change(&op)
		return op
	}

	table := []struct {
		name string
		op   Op
	}{
		{"body plain", baseBody},
		{"body encrypted version 1", examples[6]},
		{"body encrypted version 2", with(examples[6], func(op *Op) { op.EncryptionVersion = 2 })},
		{"body maximum size", with(baseBody, func(op *Op) { op.StoredSize = MaxStoredBytes })},
		{"put with metadata", basePut},
		{"put without content type or metadata", with(basePut, func(op *Op) {
			op.ContentType = DefaultContentType
			op.UserMeta = nil
		})},
		{"put with system header only", with(basePut, func(op *Op) {
			op.UserMeta = nil
			op.SystemMeta = map[string]string{"expires": "Wed, 21 Oct 2026 07:28:00 GMT"}
		})},
		{"put encrypted", baseEncPut},
		{"put encrypted version 2", with(baseEncPut, func(op *Op) { op.EncryptionVersion = 2 })},
		{"put multipart etag", examples[8]},
		{"put empty object", examples[9]},
		{"put zero timestamp", with(basePut, func(op *Op) { op.Timestamp = 0 })},
		{"put maximum timestamp", with(basePut, func(op *Op) { op.Timestamp = MaxUnixTimestamp })},
		{"put maximum message id", with(basePut, func(op *Op) { op.BodyMsgID = MaxMsgID })},
		{"put system writer", with(basePut, func(op *Op) { op.Writer = "sys.sweeper" })},
		{"put empty metadata value", with(basePut, func(op *Op) { op.UserMeta = map[string]string{"empty": ""} })},
		{"put tab in metadata value", with(basePut, func(op *Op) { op.UserMeta = map[string]string{"note": "a\tb"} })},
		{"put unicode metadata value", with(basePut, func(op *Op) { op.UserMeta = map[string]string{"title": "café \U0001F600"} })},
		{"del with put version", baseDel},
		{"del without put version", with(baseDel, func(op *Op) { op.PutVersion = 0 })},
		{"reg with writer", baseReg},
		{"reg without writer", with(baseReg, func(op *Op) { op.Writer = "" })},
		{"reg format version raised", with(baseReg, func(op *Op) { op.FormatVersion = 3; op.MinReaderVersion = 2; op.Writer = "sys.upgrade" })},
		{"bcreate with writer", baseCreate},
		{"bcreate without writer", with(baseCreate, func(op *Op) { op.Writer = "" })},
		{"bcreate zero max age", with(baseCreate, func(op *Op) { op.CacheMaxAge = 0; op.DefaultEncryption = "none" })},
		{"bcreate maximum max age", with(baseCreate, func(op *Op) { op.CacheMaxAge = MaxCacheMaxAge })},
		{"bcfg with writer", baseConfig},
		{"bcfg without writer", with(baseConfig, func(op *Op) { op.Writer = "" })},
		{"bdel with writer", baseDelete},
		{"bdel without writer", with(baseDelete, func(op *Op) { op.Writer = "" })},
		{"encfg version 1", baseEncfg},
		{"encfg version 3", with(baseEncfg, func(op *Op) { op.ConfigVersion = 3 })},
		{"encfg without writer", with(baseEncfg, func(op *Op) { op.Writer = "" })},
	}
	return table
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestRoundTripEveryOp(t *testing.T) {
	covered := map[OpType]bool{}
	for _, tc := range roundTripTable(t) {
		t.Run(tc.name, func(t *testing.T) {
			header, err := Format(tc.op)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			got, err := Parse(header)
			if err != nil {
				t.Fatalf("Parse(%q): %v", header, err)
			}
			if !reflect.DeepEqual(got, tc.op) {
				t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v\nheader: %s", got, tc.op, header)
			}
			again, err := Format(got)
			if err != nil || again != header {
				t.Fatalf("second Format = %q, %v; want %q", again, err, header)
			}
		})
		covered[tc.op.Type] = true
	}
	for _, opType := range []OpType{OpBody, OpPut, OpDel, OpRegistry, OpBucketCreate, OpBucketConfig, OpBucketDelete, OpEncConfig} {
		if !covered[opType] {
			t.Fatalf("round trip table does not cover %s", opType)
		}
	}
}

func TestPutRequiresJob(t *testing.T) {
	for _, header := range []string{
		headerWithout(putHeader, "j"),
		headerWith(putHeader, "j", ""),
		headerWith(putHeader, "j", "4F9C2D1E-8B3A-4C7D-9E2F-1A6B5C4D3E2F"),
		headerWith(putHeader, "j", "4f9c2d1e-8b3a-1c7d-9e2f-1a6b5c4d3e2f"),
	} {
		if _, err := Parse(header); !isMalformed(err) {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMalformed", header, err)
		}
	}
	op := workedExampleOps(t)[5]
	op.JobID = ""
	if _, err := Format(op); !isMalformed(err) {
		t.Fatalf("Format without job id error = %v, want ErrWireMalformed", err)
	}
}

func TestBodyRequiresJob(t *testing.T) {
	for _, header := range []string{
		headerWithout(bodyHeader, "j"),
		headerWith(bodyHeader, "j", ""),
		headerWith(bodyHeader, "j", "not-a-uuid"),
		headerWith(bodyHeader, "j", strings.ToUpper("4f9c2d1e-8b3a-4c7d-9e2f-1a6b5c4d3e2f")),
	} {
		if _, err := Parse(header); !isMalformed(err) {
			t.Fatalf("Parse(%q) error = %v, want ErrWireMalformed", header, err)
		}
	}
	op := workedExampleOps(t)[4]
	op.JobID = ""
	if _, err := Format(op); !isMalformed(err) {
		t.Fatalf("Format without job id error = %v, want ErrWireMalformed", err)
	}
}

func TestEmptyObjectRules(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"nonzero size", headerWith(emptyPutHeader, "sz", "5")},
		{"wrong etag", headerWith(emptyPutHeader, "et", "9b2cf535f27731c974343645a3985328")},
		{"wrong sha256", headerWith(emptyPutHeader, "h", strings.Repeat("a", 64))},
		{"encrypted", headerWith(headerWith(headerWith(emptyPutHeader, "enc", "1"), "psz", "0"), "kid", "437ad4b129840534")},
		{"body reference without size", headerWith(emptyPutHeader, "bm", "7")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.header); !isMalformed(err) {
				t.Fatalf("Parse error = %v, want ErrWireMalformed", err)
			}
		})
	}
	if _, err := Parse(emptyPutHeader); err != nil {
		t.Fatalf("valid empty object: %v", err)
	}
}
