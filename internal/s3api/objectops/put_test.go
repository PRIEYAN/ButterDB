package objectops

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"hash/crc32"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func TestPutOpCapKeyTooLong(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	key := strings.Repeat("/", 1024)
	rec := f.put(t, key, "data", map[string]string{"Content-Type": strings.Repeat("/", 1024)})
	doc := expectError(t, rec, 400, "KeyTooLongError")
	if doc.Fields["MaxSizeAllowed"] != "1024" || doc.Fields["Size"] != "1024" {
		t.Fatalf("extra fields = %v", doc.Fields)
	}
	if len(f.writer.calls) != 0 {
		t.Fatal("writer was called for an oversized record")
	}
}

func TestPutOpCapMetadataTooLarge(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	slashes := strings.Repeat("/", 1000)
	headers := map[string]string{
		"Cache-Control":       slashes,
		"Content-Disposition": slashes,
		"Content-Language":    slashes,
		"Expires":             slashes,
	}
	rec := f.put(t, "doc.txt", "data", headers)
	doc := expectError(t, rec, 400, "MetadataTooLarge")
	size, err := strconv.Atoi(doc.Fields["Size"])
	if err != nil || size <= limits.MaxPutOpChars {
		t.Fatalf("Size = %q", doc.Fields["Size"])
	}
	want := "The encoded object record is " + doc.Fields["Size"] + " characters; the limit is 4096"
	if doc.Message != want || doc.Fields["MaxSizeAllowed"] != "4096" {
		t.Fatalf("message = %q fields = %v", doc.Message, doc.Fields)
	}
}

func TestUserMetadata2048(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantMeta   map[string]string
		wantSize   string
	}{
		{"exactly 2048 bytes", map[string]string{"X-Amz-Meta-Alpha": strings.Repeat("v", 2048-len("alpha"))}, 200, map[string]string{"alpha": strings.Repeat("v", 2048-len("alpha"))}, ""},
		{"2049 bytes", map[string]string{"X-Amz-Meta-Alpha": strings.Repeat("v", 2049-len("alpha"))}, 400, nil, "2049"},
		{"names are lower case", map[string]string{"X-Amz-Meta-MiXed": "x"}, 200, map[string]string{"mixed": "x"}, ""},
		{"empty name", map[string]string{"X-Amz-Meta-": "x"}, 400, nil, ""},
		{"control character in value", map[string]string{"X-Amz-Meta-Note": "a\x01b"}, 400, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.put(t, "doc.txt", "data", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != 200 {
				doc := parseError(t, rec.Body.String())
				if tc.wantSize != "" && (doc.Code != "MetadataTooLarge" || doc.Fields["Size"] != tc.wantSize || doc.Fields["MaxSizeAllowed"] != "2048") {
					t.Fatalf("error = %+v", doc)
				}
				return
			}
			got := f.writer.last(t).req.UserMeta
			for name, value := range tc.wantMeta {
				if got[name] != value {
					t.Fatalf("user meta %q = %q, want %q", name, got[name], value)
				}
			}
		})
	}
}

func TestUserMetadataRepeatedHeadersJoined(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r := httptest.NewRequest(http.MethodPut, "/photos/doc.txt", strings.NewReader("data"))
	r.Header.Add("X-Amz-Meta-Tag", "one")
	r.Header.Add("X-Amz-Meta-Tag", "two")
	rec := newDeadlineRecorder()
	f.handlers.PutObject(rec, r, routeFor(router.OpPutObject, "doc.txt"), fullAuth(4))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := f.writer.last(t).req.UserMeta["tag"]; got != "one,two" {
		t.Fatalf("joined value = %q", got)
	}
}

func TestRFC2047RoundTrip(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	original := "héllo wörld"
	encoded := mime.BEncoding.Encode("UTF-8", original)
	rec := f.put(t, "doc.txt", "data", map[string]string{"X-Amz-Meta-Note": encoded, "X-Amz-Meta-Plain": "ascii"})
	if rec.Code != 200 {
		t.Fatalf("put status = %d; body=%q", rec.Code, rec.Body.String())
	}
	stored := f.writer.last(t).req.UserMeta
	if stored["note"] != original || stored["plain"] != "ascii" {
		t.Fatalf("stored meta = %v", stored)
	}
	got := f.get(t, "doc.txt", nil, fullAuth(0), "")
	header := got.Header().Get("X-Amz-Meta-Note")
	if !strings.HasPrefix(header, "=?UTF-8?") {
		t.Fatalf("response header %q is not RFC 2047 encoded", header)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(header)
	if err != nil || decoded != original {
		t.Fatalf("decoded = %q, %v", decoded, err)
	}
	if got.Header().Get("X-Amz-Meta-Plain") != "ascii" {
		t.Fatalf("ascii value was encoded: %q", got.Header().Get("X-Amz-Meta-Plain"))
	}
}

func TestEntityTooLargeBeforeBody(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	reader := &countingReader{reader: strings.NewReader("never read")}
	r := httptest.NewRequest(http.MethodPut, "/photos/big.bin", reader)
	rec := newDeadlineRecorder()
	proposed := int64(limits.MaxStoredObjectBytes) + 1
	f.handlers.PutObject(rec, r, routeFor(router.OpPutObject, "big.bin"), fullAuth(proposed))
	doc := expectError(t, rec, 400, "EntityTooLarge")
	if doc.Fields["MaxSizeAllowed"] != "1992294400" || doc.Fields["ProposedSize"] != strconv.FormatInt(proposed, 10) {
		t.Fatalf("fields = %v", doc.Fields)
	}
	if reader.reads != 0 {
		t.Fatalf("body was read %d times", reader.reads)
	}
	if len(f.writer.calls) != 0 {
		t.Fatal("writer was called")
	}
}

func TestEncryptedSizeLimit(t *testing.T) {
	const justUnderLimit = int64(limits.MaxStoredObjectBytes) - 1
	encryptedDefault := map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionTDE1}}
	cases := []struct {
		name     string
		infos    map[bucket.BucketName]bucket.Info
		headers  map[string]string
		size     int64
		wantCode string
	}{
		{"plaintext at the limit", nil, nil, limits.MaxStoredObjectBytes, ""},
		{"plaintext over the limit", nil, nil, limits.MaxStoredObjectBytes + 1, "EntityTooLarge"},
		{"aes256 just under the limit overflows once encrypted", nil, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, justUnderLimit, "EntityTooLarge"},
		{"tde1 bucket default just under the limit overflows once encrypted", encryptedDefault, nil, justUnderLimit, "EntityTooLarge"},
		{"aes256 small object", nil, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, 1024, ""},
		{"plaintext just under the limit is fine", nil, nil, justUnderLimit, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: tc.infos})
			r := httptest.NewRequest(http.MethodPut, "/photos/big.bin", strings.NewReader("x"))
			for name, value := range tc.headers {
				r.Header.Set(name, value)
			}
			rec := newDeadlineRecorder()
			f.handlers.PutObject(rec, r, routeFor(router.OpPutObject, "big.bin"), fullAuth(tc.size))
			if tc.wantCode == "" {
				if rec.Code != 200 {
					t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
				}
				return
			}
			expectError(t, rec, 400, tc.wantCode)
		})
	}
}

func TestSSEValues(t *testing.T) {
	encryptedDefault := map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionTDE1}}
	cases := []struct {
		name         string
		infos        map[bucket.BucketName]bucket.Info
		header       string
		wantStatus   int
		wantRequest  string
		wantErrorMsg string
	}{
		{"absent on a plain bucket", nil, "", 200, "", ""},
		{"absent on a tde1 bucket", encryptedDefault, "", 200, "", ""},
		{"AES256 on a plain bucket", nil, "AES256", 200, bucket.EncryptionTDE1, ""},
		{"AES256 on a tde1 bucket", encryptedDefault, "AES256", 200, bucket.EncryptionTDE1, ""},
		{"aws:kms", nil, "aws:kms", 400, "", "Server Side Encryption with aws:kms is not supported; use AES256"},
		{"aws:kms:dsse", nil, "aws:kms:dsse", 400, "", "Server Side Encryption with aws:kms:dsse is not supported; use AES256"},
		{"unknown value", encryptedDefault, "AES128", 400, "", "Server Side Encryption with AES128 is not supported; use AES256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: tc.infos})
			headers := map[string]string{}
			if tc.header != "" {
				headers["X-Amz-Server-Side-Encryption"] = tc.header
			}
			rec := f.put(t, "doc.txt", "data", headers)
			if tc.wantStatus != 200 {
				doc := expectError(t, rec, tc.wantStatus, "InvalidArgument")
				if doc.Message != tc.wantErrorMsg {
					t.Fatalf("message = %q", doc.Message)
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := f.writer.last(t).req.Encryption; got != tc.wantRequest {
				t.Fatalf("request encryption = %q, want %q", got, tc.wantRequest)
			}
		})
	}
}

func TestPutEchoesEncryptionHeaderOfCommittedObject(t *testing.T) {
	cases := []struct {
		name      string
		encrypted bool
		want      string
	}{
		{"encrypted object", true, "AES256"},
		{"plaintext object", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			f.writer.meta = func(req object.PutRequest, data []byte) object.ObjectMeta {
				return object.ObjectMeta{ETag: "9b2cf535f27731c974343645a3985328", Encrypted: tc.encrypted}
			}
			rec := f.put(t, "doc.txt", "data", nil)
			if got := rec.Header().Get("X-Amz-Server-Side-Encryption"); got != tc.want {
				t.Fatalf("sse header = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get("ETag"); got != `"9b2cf535f27731c974343645a3985328"` {
				t.Fatalf("ETag = %q", got)
			}
		})
	}
}

func TestCannedACLMatrix(t *testing.T) {
	publicRead := map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPublicRead, DefaultEncryption: bucket.EncryptionNone}}
	cases := []struct {
		name       string
		infos      map[bucket.BucketName]bucket.Info
		acl        string
		wantStatus int
	}{
		{"absent", nil, "", 200},
		{"private", nil, "private", 200},
		{"bucket-owner-full-control", nil, "bucket-owner-full-control", 200},
		{"bucket-owner-read", nil, "bucket-owner-read", 200},
		{"public-read in a private bucket", nil, "public-read", 400},
		{"public-read in a public-read bucket", publicRead, "public-read", 200},
		{"public-read-write", publicRead, "public-read-write", 400},
		{"authenticated-read", publicRead, "authenticated-read", 400},
		{"aws-exec-read", nil, "aws-exec-read", 400},
		{"log-delivery-write", nil, "log-delivery-write", 400},
		{"unknown canned value", nil, "bogus", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: tc.infos})
			headers := map[string]string{}
			if tc.acl != "" {
				headers["X-Amz-Acl"] = tc.acl
			}
			rec := f.put(t, "doc.txt", "data", headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 400 {
				expectError(t, rec, 400, "AccessControlListNotSupported")
			}
		})
	}
}

func TestGrantHeadersRefused(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	rec := f.put(t, "doc.txt", "data", map[string]string{"X-Amz-Grant-Read": `uri="http://acs.amazonaws.com/groups/global/AllUsers"`})
	expectError(t, rec, 400, "AccessControlListNotSupported")
}

func TestPutUnsupportedFeatures(t *testing.T) {
	cases := []struct {
		name     string
		headers  map[string]string
		query    string
		wantCode string
		status   int
	}{
		{"sse-c", map[string]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm": "AES256"}, "", "NotImplemented", 501},
		{"kms key id", map[string]string{"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id": "k"}, "", "NotImplemented", 501},
		{"bucket key enabled", map[string]string{"X-Amz-Server-Side-Encryption-Bucket-Key-Enabled": "true"}, "", "NotImplemented", 501},
		{"object lock", map[string]string{"X-Amz-Object-Lock-Mode": "GOVERNANCE"}, "", "NotImplemented", 501},
		{"website redirect", map[string]string{"X-Amz-Website-Redirect-Location": "/x"}, "", "NotImplemented", 501},
		{"tagging", map[string]string{"X-Amz-Tagging": "a=b"}, "", "NotImplemented", 501},
		{"storage class", map[string]string{"X-Amz-Storage-Class": "GLACIER"}, "", "InvalidStorageClass", 400},
		{"version id other than null", nil, "?versionId=3", "InvalidArgument", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.do(t, f.handlers.PutObject, router.OpPutObject, "doc.txt", request{
				method: http.MethodPut, target: "/photos/doc.txt" + tc.query, body: "data", headers: tc.headers, auth: fullAuth(4),
			})
			expectError(t, rec, tc.status, tc.wantCode)
		})
	}
	t.Run("storage class STANDARD and version id null are accepted", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{})
		rec := f.do(t, f.handlers.PutObject, router.OpPutObject, "doc.txt", request{
			method: http.MethodPut, target: "/photos/doc.txt?versionId=null", body: "data", headers: map[string]string{"X-Amz-Storage-Class": "STANDARD"}, auth: fullAuth(4),
		})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestPutRequiresLength(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	rec := f.do(t, f.handlers.PutObject, router.OpPutObject, "doc.txt", request{
		method: http.MethodPut, target: "/photos/doc.txt", body: "data", auth: fullAuth(-1),
	})
	expectError(t, rec, 411, "MissingContentLength")
}

func TestPutPassesRequestFields(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	headers := map[string]string{
		"Content-Type":        "image/png",
		"Cache-Control":       "max-age=60",
		"Content-Disposition": "attachment",
		"Content-Encoding":    "gzip, aws-chunked",
		"Content-Language":    "en",
		"Expires":             "Wed, 21 Oct 2037 07:28:00 GMT",
		"If-Match":            `"abc"`,
	}
	rec := f.put(t, "a/b.png", "data", headers)
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	call := f.writer.last(t)
	if call.req.Bucket != testBucket || string(call.req.Key) != "a/b.png" || call.req.ContentLength != 4 || call.req.AccessKeyID != "BDBKEXAMPLE" {
		t.Fatalf("request = %+v", call.req)
	}
	wantSystem := map[string]string{
		"cache-control":       "max-age=60",
		"content-disposition": "attachment",
		"content-encoding":    "gzip",
		"content-language":    "en",
		"expires":             "Wed, 21 Oct 2037 07:28:00 GMT",
	}
	for name, value := range wantSystem {
		if call.req.SystemMeta[name] != value {
			t.Fatalf("system %q = %q, want %q", name, call.req.SystemMeta[name], value)
		}
	}
	if call.req.ContentType != "image/png" || call.req.IfMatch != `"abc"` || string(call.body) != "data" {
		t.Fatalf("request = %+v body=%q", call.req, call.body)
	}
}

func TestPutSystemHeaderLimits(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"1024 bytes accepted", map[string]string{"Content-Language": strings.Repeat("l", 1024)}, 200},
		{"1025 bytes refused", map[string]string{"Content-Language": strings.Repeat("l", 1025)}, 400},
		{"content type over 1024", map[string]string{"Content-Type": strings.Repeat("t", 1025)}, 400},
		{"only aws-chunked leaves no content encoding", map[string]string{"Content-Encoding": "aws-chunked"}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.put(t, "doc.txt", "data", tc.headers)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 400 {
				expectError(t, rec, 400, "InvalidArgument")
			}
		})
	}
	f := newFixture(t, fixtureOptions{})
	f.put(t, "doc.txt", "data", map[string]string{"Content-Encoding": "aws-chunked"})
	if _, present := f.writer.last(t).req.SystemMeta["content-encoding"]; present {
		t.Fatal("aws-chunked was stored as a content encoding")
	}
}

func TestPutConditions(t *testing.T) {
	cases := []struct {
		name        string
		headers     map[string]string
		wantStatus  int
		wantNoneAny bool
	}{
		{"if-none-match star", map[string]string{"If-None-Match": "*"}, 200, true},
		{"if-none-match other value", map[string]string{"If-None-Match": `"abc"`}, 400, false},
		{"none", nil, 200, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.put(t, "doc.txt", "data", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == 200 && f.writer.last(t).req.IfNoneMatchAny != tc.wantNoneAny {
				t.Fatalf("IfNoneMatchAny = %v", f.writer.last(t).req.IfNoneMatchAny)
			}
		})
	}
	t.Run("precondition failure from the writer is a 412", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{})
		f.writer.err = errs.ErrPreconditionFailed
		expectError(t, f.put(t, "doc.txt", "data", map[string]string{"If-None-Match": "*"}), 412, "PreconditionFailed")
	})
}

func TestContentMD5(t *testing.T) {
	sum := md5.Sum([]byte("data"))
	good := base64.StdEncoding.EncodeToString(sum[:])
	wrong := md5.Sum([]byte("other"))
	cases := []struct {
		name       string
		header     string
		wantStatus int
		wantCode   string
		wantWrites int
	}{
		{"matching digest", good, 200, "", 1},
		{"mismatching digest", base64.StdEncoding.EncodeToString(wrong[:]), 400, "BadDigest", 1},
		{"not base64", "%%%", 400, "InvalidDigest", 0},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("short")), 400, "InvalidDigest", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.put(t, "doc.txt", "data", map[string]string{"Content-MD5": tc.header})
			if tc.wantCode == "" {
				if rec.Code != 200 {
					t.Fatalf("status = %d", rec.Code)
				}
			} else {
				expectError(t, rec, tc.wantStatus, tc.wantCode)
			}
			if len(f.writer.calls) != tc.wantWrites {
				t.Fatalf("writer calls = %d, want %d", len(f.writer.calls), tc.wantWrites)
			}
		})
	}
}

func TestPutEchoesVerifiedChecksum(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	checksum := crc32.ChecksumIEEE([]byte("data"))
	value := base64.StdEncoding.EncodeToString([]byte{byte(checksum >> 24), byte(checksum >> 16), byte(checksum >> 8), byte(checksum)})
	rec := f.put(t, "doc.txt", "data", map[string]string{"X-Amz-Checksum-Crc32": value})
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Amz-Checksum-Crc32"); got != value {
		t.Fatalf("echoed checksum = %q, want %q", got, value)
	}
	bad := f.put(t, "doc.txt", "other", map[string]string{"X-Amz-Checksum-Crc32": value})
	expectError(t, bad, 400, "BadDigest")
}

func TestPutAccess(t *testing.T) {
	cases := []struct {
		name     string
		auth     func() (bool, []string, []string)
		wantCode string
		status   int
	}{
		{"read only key", func() (bool, []string, []string) { return false, []string{"*"}, []string{"read"} }, "AccessDenied", 403},
		{"bucket outside scope", func() (bool, []string, []string) { return false, []string{"other"}, []string{"write"} }, "AccessDenied", 403},
		{"anonymous", func() (bool, []string, []string) { return true, nil, nil }, "AccessDenied", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			anonymous, buckets, actions := tc.auth()
			auth := anonymousAuth()
			if !anonymous {
				auth = authFor(buckets, actions, 4)
			}
			rec := f.do(t, f.handlers.PutObject, router.OpPutObject, "doc.txt", request{method: http.MethodPut, target: "/photos/doc.txt", body: "data", auth: auth})
			expectError(t, rec, tc.status, tc.wantCode)
			if len(f.writer.calls) != 0 {
				t.Fatal("writer was called")
			}
		})
	}
}

func TestPutCommitSurvivesClientDisconnect(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := f.do(t, f.handlers.PutObject, router.OpPutObject, "doc.txt", request{
		method: http.MethodPut, target: "/photos/doc.txt", body: "data", auth: fullAuth(4), ctx: ctx,
	})
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if err := f.writer.last(t).ctx.Err(); err != nil {
		t.Fatalf("commit context was cancelled: %v", err)
	}
}

func TestPutErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{"no such bucket", errs.ErrNoSuchBucket, 404, "NoSuchBucket"},
		{"slow down", errs.ErrSlowDown, 503, "SlowDown"},
		{"internal", context.DeadlineExceeded, 500, "InternalError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			f.writer.err = tc.err
			expectError(t, f.put(t, "doc.txt", "data", nil), tc.status, tc.wantCode)
		})
	}
}

func TestControlCharacterKeyRejected(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		wantCode string
		message  string
	}{
		{"nul", "a\x00b", "InvalidArgument", "Object key contains a control character"},
		{"unit separator", "a\x1fb", "InvalidArgument", "Object key contains a control character"},
		{"newline", "a\nb", "InvalidArgument", "Object key contains a control character"},
		{"delete", "a\x7fb", "InvalidArgument", "Object key contains a control character"},
		{"invalid utf-8", "a\xffb", "InvalidArgument", "Object key is not valid UTF-8"},
		{"too long", strings.Repeat("k", 1025), "KeyTooLongError", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.do(t, f.handlers.PutObject, router.OpPutObject, tc.key, request{method: http.MethodPut, target: "/photos/x", body: "data", auth: fullAuth(4)})
			doc := expectError(t, rec, 400, tc.wantCode)
			if tc.message != "" && doc.Message != tc.message {
				t.Fatalf("message = %q", doc.Message)
			}
			if len(f.writer.calls) != 0 {
				t.Fatal("writer was called")
			}
			del := f.do(t, f.handlers.DeleteObject, router.OpDeleteObject, tc.key, request{method: http.MethodDelete, target: "/photos/x", auth: fullAuth(0)})
			expectError(t, del, 400, tc.wantCode)
			if len(f.remover.calls) != 0 {
				t.Fatal("remover was called")
			}
		})
	}
}
