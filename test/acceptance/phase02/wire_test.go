package phase02

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestEveryOpParsesAfterFormatting(t *testing.T) {
	covered := map[wire.OpType]bool{}
	for _, tc := range workedExamples {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := wire.Parse(tc.header)
			if err != nil {
				t.Fatalf("parse worked example: %v", err)
			}
			if parsed.Type != tc.opType {
				t.Fatalf("type = %q, want %q", parsed.Type, tc.opType)
			}
			formatted, err := wire.Format(parsed)
			if err != nil {
				t.Fatalf("format: %v", err)
			}
			if formatted != tc.header {
				t.Fatalf("format is not byte exact\n got: %s\nwant: %s", formatted, tc.header)
			}
			again, err := wire.Parse(formatted)
			if err != nil {
				t.Fatalf("parse formatted: %v", err)
			}
			if !reflect.DeepEqual(again, parsed) {
				t.Fatalf("round trip changed the op\n got: %+v\nwant: %+v", again, parsed)
			}
			if !wire.Known(parsed.Type) {
				t.Fatalf("%q is not known", parsed.Type)
			}
			if wire.OnDocument(parsed.Type) != (parsed.Type == wire.OpBody) {
				t.Fatalf("OnDocument(%q) = %v", parsed.Type, wire.OnDocument(parsed.Type))
			}
			covered[parsed.Type] = true
		})
	}
	for _, opType := range []wire.OpType{
		wire.OpBody, wire.OpPut, wire.OpDel, wire.OpRegistry,
		wire.OpBucketCreate, wire.OpBucketConfig, wire.OpBucketDelete, wire.OpEncConfig,
	} {
		if !covered[opType] {
			t.Errorf("no worked example covers %q", opType)
		}
	}
}

func TestOpVariantsRoundTripThroughFormat(t *testing.T) {
	cases := []struct {
		name string
		op   wire.Op
	}{
		{"put with key needing every escape", wire.Op{
			Type: wire.OpPut, JobID: putJob, Key: " lead|pipe=eq%pct+plus\nnl\U0001F600 ", BodyMsgID: 9, StoredSize: 5,
			PlaintextSize: 5, ETag: putETag, SHA256: putSHA256, ContentType: wire.DefaultContentType,
			Writer: putWriter, Timestamp: 1,
		}},
		{"put with both metadata maps", wire.Op{
			Type: wire.OpPut, JobID: putJob, Key: "k", BodyMsgID: 9, StoredSize: 5, PlaintextSize: 5,
			ETag: putETag, SHA256: putSHA256, ContentType: "text/plain",
			UserMeta: map[string]string{"a": "1", "b": "two words"}, SystemMeta: map[string]string{"cache-control": "no-cache"},
			Writer: putWriter, Timestamp: 253402300799,
		}},
		{"encrypted put with a later encryption version", wire.Op{
			Type: wire.OpPut, JobID: putJob, Key: "k", BodyMsgID: 9, StoredSize: 100, PlaintextSize: 34, Encrypted: true,
			EncryptionVersion: 2, KeyID: "437ad4b129840534", ETag: putETag, SHA256: putSHA256,
			ContentType: wire.DefaultContentType, Writer: putWriter, Timestamp: 5,
		}},
		{"encrypted body with a later encryption version", wire.Op{
			Type: wire.OpBody, JobID: putJob, StoredSize: 100, Encrypted: true, EncryptionVersion: 2,
		}},
		{"encryption config with a later config version", wire.Op{
			Type: wire.OpEncConfig, JobID: putJob, BucketChannelID: 7, KeyID: "437ad4b129840534",
			WrappedKey: make([]byte, 72), KeyCheck: make([]byte, 61),
			KEKFingerprint: "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6", ConfigVersion: 3, Writer: "dev.x", Timestamp: 9,
		}},
		{"registry marker without writer", wire.Op{
			Type: wire.OpRegistry, DeploymentID: "6pzyoxbfwcf6ubwcq5gpvjg5c4", FormatVersion: 1, MinReaderVersion: 1,
			KEKFingerprint: "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6", PrimaryUserID: 5, Timestamp: 3,
		}},
		{"bucket configuration at the cache ceiling", wire.Op{
			Type: wire.OpBucketConfig, JobID: putJob, Bucket: "photos", BucketChannelID: 11, Policy: "private",
			DefaultEncryption: "none", CacheMaxAge: wire.MaxCacheMaxAge, Writer: "dev.x", Timestamp: 3,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := wire.Format(tc.op)
			if err != nil {
				t.Fatalf("format: %v", err)
			}
			got, err := wire.Parse(text)
			if err != nil {
				t.Fatalf("parse %q: %v", text, err)
			}
			if !reflect.DeepEqual(got, tc.op) {
				t.Fatalf("round trip changed the op\n got: %+v\nwant: %+v", got, tc.op)
			}
		})
	}
}

func TestMalformedWireDataReturnsTheSpecifiedError(t *testing.T) {
	longHeader := "TDX1|t=put|" + strings.Repeat("zz=1|", wire.MaxTextChars/5)
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"empty text", "", wire.ErrWireMissingHeader},
		{"whitespace only", "  \n body", wire.ErrWireMissingHeader},
		{"newer grammar prefix", "TDX2|t=put|j=x", wire.ErrWireBadVersion},
		{"prefix with trailing characters", "TDX10|t=put", wire.ErrWireBadVersion},
		{"prefix on a foreign header", "hello|t=put", wire.ErrWireBadVersion},
		{"prefix only", "TDX1", wire.ErrWireMalformed},
		{"plain text", "hello", wire.ErrWireMalformed},
		{"equals at index zero", "TDX1|=x|t=put", wire.ErrWireMalformed},
		{"pair without equals", "TDX1|t=put|novalue", wire.ErrWireMalformed},
		{"empty pair", "TDX1||t=put", wire.ErrWireMalformed},
		{"missing type", "TDX1|j=" + putJob, wire.ErrWireMalformed},
		{"empty type", "TDX1|t=|j=" + putJob, wire.ErrWireMalformed},
		{"empty key", strings.Replace(putTemplate, "k=a.txt", "k=", 1), wire.ErrWireMalformed},
		{"key with NUL", strings.Replace(putTemplate, "k=a.txt", "k=a%00b", 1), wire.ErrWireMalformed},
		{"key with invalid utf8", strings.Replace(putTemplate, "k=a.txt", "k=%ff%fe", 1), wire.ErrWireMalformed},
		{"key over 1024 bytes", strings.Replace(putTemplate, "k=a.txt", "k="+strings.Repeat("a", 1025), 1), wire.ErrWireMalformed},
		{"key with bad percent escape", strings.Replace(putTemplate, "k=a.txt", "k=a%zz", 1), wire.ErrWireMalformed},
		{"negative stored size", strings.Replace(putTemplate, "sz=5", "sz=-1", 1), wire.ErrWireMalformed},
		{"plaintext size without encryption", strings.Replace(putTemplate, "sz=5", "sz=5|psz=10", 1), wire.ErrWireMalformed},
		{"encrypted without key id", strings.Replace(putTemplate, "sz=5", "sz=100|psz=34|enc=1", 1), wire.ErrWireMalformed},
		{"empty object with a size", strings.Replace(putTemplate, "bm=8811", "bm=0", 1), wire.ErrWireMalformed},
		{"body message id over the ceiling", strings.Replace(putTemplate, "bm=8811", "bm=2147483648", 1), wire.ErrWireMalformed},
		{"timestamp one past the ceiling", strings.Replace(putTemplate, "ts=1763000200", "ts=253402300800", 1), wire.ErrWireMalformed},
		{"negative timestamp", strings.Replace(putTemplate, "ts=1763000200", "ts=-1", 1), wire.ErrWireMalformed},
		{"job id not a uuid", strings.Replace(putTemplate, putJob, "not-a-job", 1), wire.ErrWireMalformed},
		{"job id uppercase", strings.Replace(putTemplate, putJob, strings.ToUpper(putJob), 1), wire.ErrWireMalformed},
		{"etag uppercase", strings.Replace(putTemplate, putETag, strings.ToUpper(putETag), 1), wire.ErrWireMalformed},
		{"sha256 too short", strings.Replace(putTemplate, putSHA256, putSHA256[:63], 1), wire.ErrWireMalformed},
		{"writer without a known prefix", strings.Replace(putTemplate, putWriter, "usr.abc", 1), wire.ErrWireMalformed},
		{"missing writer", strings.Replace(putTemplate, "|w="+putWriter, "", 1), wire.ErrWireMalformed},
		{"content type with a control byte", strings.Replace(putTemplate, "|bm=", "|ct=a%09b|bm=", 1), wire.ErrWireMalformed},
		{"user metadata name in uppercase", strings.Replace(putTemplate, "|bm=", "|m.Name=v|bm=", 1), wire.ErrWireMalformed},
		{"user metadata name written twice", strings.Replace(putTemplate, "|bm=", "|m.a=1|m.%61=2|bm=", 1), wire.ErrWireMalformed},
		{"user metadata name empty", strings.Replace(putTemplate, "|bm=", "|m.=v|bm=", 1), wire.ErrWireMalformed},
		{"user metadata value with a line feed", strings.Replace(putTemplate, "|bm=", "|m.a=x%0Ay|bm=", 1), wire.ErrWireMalformed},
		{"user metadata over 2048 bytes", strings.Replace(putTemplate, "|bm=", "|m.a="+strings.Repeat("v", 2048)+"|bm=", 1), wire.ErrWireMalformed},
		{"system metadata name not on the list", strings.Replace(putTemplate, "|bm=", "|s.nope=v|bm=", 1), wire.ErrWireMalformed},
		{"put without a job", strings.Replace(putTemplate, "|j="+putJob, "", 1), wire.ErrWireMalformed},
		{"body without a job", "TDX1|t=body|sz=5", wire.ErrWireMalformed},
		{"body with a zero size", "TDX1|t=body|j=" + putJob + "|sz=0", wire.ErrWireMalformed},
		{"body with a version but no encryption", "TDX1|t=body|j=" + putJob + "|sz=5|ev=2", wire.ErrWireMalformed},
		{"shared size on a registry marker", workedExamples[0].header + "|sz=-1", wire.ErrWireMalformed},
		{"registry minimum reader above the format version", strings.Replace(workedExamples[0].header, "mr=1", "mr=2", 1), wire.ErrWireMalformed},
		{"wrapped key one byte short", strings.Replace(workedExamples[2].header, "|wrap=ro", "|wrap=r", 1), wire.ErrWireMalformed},
		{"bucket name too short", strings.Replace(workedExamples[1].header, "b=photos", "b=ab", 1), wire.ErrWireMalformed},
		{"cache max age over a year", strings.Replace(workedExamples[1].header, "cma=3600", "cma=31536001", 1), wire.ErrWireMalformed},
		{"delete with put version zero", strings.Replace(workedExamples[10].header, "pv=8812", "pv=0", 1), wire.ErrWireMalformed},
		{"header over the text limit", longHeader, wire.ErrWireTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wire.Parse(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFormatRefusesWhatParseWouldRefuse(t *testing.T) {
	parsed, err := wire.Parse(putTemplate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(op *wire.Op)
		want   error
	}{
		{"empty key", func(op *wire.Op) { op.Key = "" }, wire.ErrWireMalformed},
		{"key with NUL", func(op *wire.Op) { op.Key = "a\x00" }, wire.ErrWireMalformed},
		{"job id not a uuid", func(op *wire.Op) { op.JobID = "x" }, wire.ErrWireMalformed},
		{"negative stored size", func(op *wire.Op) { op.StoredSize = -1 }, wire.ErrWireMalformed},
		{"timestamp past the ceiling", func(op *wire.Op) { op.Timestamp = wire.MaxUnixTimestamp + 1 }, wire.ErrWireMalformed},
		{"key and metadata past the text limit", func(op *wire.Op) {
			op.Key = strings.Repeat("é", wire.MaxKeyBytes/2)
			op.UserMeta = map[string]string{"a": strings.Repeat("é", wire.MaxUserMetaBytes/2-1)}
		}, wire.ErrWireTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := parsed
			tc.mutate(&op)
			if _, err := wire.Format(op); !errors.Is(err, tc.want) {
				t.Fatalf("Format error = %v, want %v", err, tc.want)
			}
		})
	}
}
