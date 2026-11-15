package objectops

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const archiveBucket = "archive"

func newCopyFixture(t *testing.T, infos map[bucket.BucketName]bucket.Info, mutate func(*object.ObjectMeta)) *fixture {
	t.Helper()
	if infos == nil {
		infos = map[bucket.BucketName]bucket.Info{
			testBucket:    privateBucket(),
			archiveBucket: {Name: archiveBucket, ChannelID: 9, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone},
		}
	}
	f := newFixture(t, fixtureOptions{infos: infos})
	f.store.add(testBucket, "src.txt", []byte(sampleContent), func(m *object.ObjectMeta) {
		m.UserMeta = map[string]string{"color": "blue"}
		m.SystemMeta = map[string]string{"cache-control": "max-age=60"}
		m.ContentType = "image/png"
		if mutate != nil {
			mutate(m)
		}
	})
	f.store.add(archiveBucket, "kept.txt", []byte("kept"), nil)
	return f
}

func (f *fixture) copyObject(t *testing.T, dstKey, source string, headers map[string]string) *deadlineRecorder {
	t.Helper()
	merged := map[string]string{"X-Amz-Copy-Source": source}
	for name, value := range headers {
		merged[name] = value
	}
	return f.do(t, f.handlers.CopyObject, router.OpCopyObject, dstKey, request{
		method: http.MethodPut, target: "/photos/" + dstKey, headers: merged, auth: fullAuth(0),
	})
}

func TestCopyObjectSuccess(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	rec := f.copyObject(t, "dst.txt", "photos/src.txt", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`+"\n") {
		t.Fatalf("declaration must come first: %q", body)
	}
	for _, want := range []string{"<CopyObjectResult", "<ETag>&#34;781e5e245d69b566979b86e28d23f2c7&#34;</ETag>", "<LastModified>2027-01-15T08:00:00.000Z</LastModified>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q lacks %q", body, want)
		}
	}
	call := f.writer.last(t)
	if string(call.body) != sampleContent || call.req.ContentLength != 10 || string(call.req.Key) != "dst.txt" || call.req.Bucket != testBucket {
		t.Fatalf("put call = %+v body=%q", call.req, call.body)
	}
	if call.req.ContentType != "image/png" || call.req.UserMeta["color"] != "blue" || call.req.SystemMeta["cache-control"] != "max-age=60" {
		t.Fatalf("COPY directive lost source attributes: %+v", call.req)
	}
	for _, opened := range f.store.opened {
		if !opened.closed {
			t.Fatal("the source was not closed")
		}
	}
}

func TestCopyObjectAcrossBuckets(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	rec := f.copyObject(t, "dst.txt", "/archive/kept.txt", nil)
	if rec.Code != 200 || string(f.writer.last(t).body) != "kept" {
		t.Fatalf("status = %d body = %q", rec.Code, f.writer.last(t).body)
	}
}

func TestCopyObjectMetadataDirective(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	rec := f.copyObject(t, "dst.txt", "photos/src.txt", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"Content-Type":             "text/csv",
		"X-Amz-Meta-Owner":         "ops",
	})
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	req := f.writer.last(t).req
	if req.ContentType != "text/csv" || req.UserMeta["owner"] != "ops" || len(req.UserMeta) != 1 || len(req.SystemMeta) != 0 {
		t.Fatalf("REPLACE directive request = %+v", req)
	}
	expectError(t, f.copyObject(t, "dst.txt", "photos/src.txt", map[string]string{"X-Amz-Metadata-Directive": "MERGE"}), 400, "InvalidArgument")
}

func TestCopySelfIllegal(t *testing.T) {
	encryptedDefault := map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionTDE1}}
	cases := []struct {
		name         string
		infos        map[bucket.BucketName]bucket.Info
		sourceCipher bool
		headers      map[string]string
		wantStatus   int
	}{
		{"copy directive without any change", nil, false, nil, 400},
		{"copy directive with encrypted source and no change", nil, true, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, 400},
		{"replace directive", nil, false, map[string]string{"X-Amz-Metadata-Directive": "REPLACE", "Content-Type": "text/csv"}, 200},
		{"encryption added with the header", nil, false, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, 200},
		{"encryption added by the bucket default", encryptedDefault, false, nil, 200},
		{"source encrypted but destination would be plain", nil, true, nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t, tc.infos, func(m *object.ObjectMeta) { m.Encrypted = tc.sourceCipher })
			rec := f.copyObject(t, "src.txt", "photos/src.txt", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 400 {
				doc := parseError(t, rec.Body.String())
				if doc.Code != "InvalidRequest" || !strings.HasPrefix(doc.Message, "This copy request is illegal because it is trying to copy an object to itself") {
					t.Fatalf("error = %+v", doc)
				}
				if len(f.writer.calls) != 0 {
					t.Fatal("writer was called for an illegal self copy")
				}
			}
		})
	}
}

func TestCopyEncryptionNotInherited(t *testing.T) {
	cases := []struct {
		name          string
		sourceCipher  bool
		headers       map[string]string
		wantRequest   string
		wantSSEHeader bool
	}{
		{"encrypted source into a plain bucket stays plain", true, nil, "", false},
		{"plain source with AES256 becomes encrypted", false, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, bucket.EncryptionTDE1, true},
		{"encrypted source with AES256 stays requested", true, map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, bucket.EncryptionTDE1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t, nil, func(m *object.ObjectMeta) { m.Encrypted = tc.sourceCipher })
			rec := f.copyObject(t, "dst.txt", "photos/src.txt", tc.headers)
			if rec.Code != 200 {
				t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
			}
			if got := f.writer.last(t).req.Encryption; got != tc.wantRequest {
				t.Fatalf("request encryption = %q, want %q", got, tc.wantRequest)
			}
			if got := rec.Header().Get("X-Amz-Server-Side-Encryption"); (got == "AES256") != tc.wantSSEHeader {
				t.Fatalf("sse header = %q, want present=%v", got, tc.wantSSEHeader)
			}
		})
	}
	t.Run("destination bucket default encrypts without a header", func(t *testing.T) {
		infos := map[bucket.BucketName]bucket.Info{
			testBucket:    {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionTDE1},
			archiveBucket: {Name: archiveBucket, ChannelID: 9, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone},
		}
		f := newCopyFixture(t, infos, nil)
		rec := f.copyObject(t, "dst.txt", "archive/kept.txt", nil)
		if rec.Code != 200 || rec.Header().Get("X-Amz-Server-Side-Encryption") != "AES256" {
			t.Fatalf("status = %d sse = %q", rec.Code, rec.Header().Get("X-Amz-Server-Side-Encryption"))
		}
	})
}

func TestCopyObjectSizeAndRecordLimits(t *testing.T) {
	t.Run("encrypted destination overflows before any read", func(t *testing.T) {
		f := newCopyFixture(t, nil, func(m *object.ObjectMeta) { m.PlaintextSize = limits.MaxStoredObjectBytes - 1 })
		rec := f.copyObject(t, "dst.txt", "photos/src.txt", map[string]string{"X-Amz-Server-Side-Encryption": "AES256"})
		expectError(t, rec, 400, "EntityTooLarge")
		if len(f.writer.calls) != 0 {
			t.Fatal("writer was called")
		}
	})
	t.Run("plain destination at the limit passes the size check", func(t *testing.T) {
		f := newCopyFixture(t, nil, func(m *object.ObjectMeta) { m.PlaintextSize = limits.MaxStoredObjectBytes })
		f.writer.err = errors.New("stop after validation")
		rec := f.copyObject(t, "dst.txt", "photos/src.txt", nil)
		if !strings.Contains(rec.Body.String(), "<Code>InternalError</Code>") {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})
	t.Run("source record too large for the destination", func(t *testing.T) {
		slashes := strings.Repeat("/", 1000)
		f := newCopyFixture(t, nil, func(m *object.ObjectMeta) {
			m.SystemMeta = map[string]string{"cache-control": slashes, "content-disposition": slashes, "content-language": slashes, "expires": slashes}
		})
		expectError(t, f.copyObject(t, "dst.txt", "photos/src.txt", nil), 400, "MetadataTooLarge")
	})
}

func TestCopyObjectSourceFailures(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		headers    map[string]string
		wantStatus int
		wantCode   string
		wantKey    string
	}{
		{"missing key names the source", "photos/absent.txt", nil, 404, "NoSuchKey", "absent.txt"},
		{"missing source bucket", "ghost/kept.txt", nil, 404, "NoSuchBucket", ""},
		{"no key", "photos", nil, 400, "InvalidArgument", ""},
		{"empty key", "photos/", nil, 400, "InvalidArgument", ""},
		{"invalid bucket", "A/key", nil, 400, "InvalidArgument", ""},
		{"bad escape", "photos/%zz", nil, 400, "InvalidArgument", ""},
		{"version id", "photos/src.txt?versionId=2", nil, 400, "InvalidArgument", ""},
		{"destination if-match", "photos/src.txt", map[string]string{"If-Match": "*"}, 501, "NotImplemented", ""},
		{"destination if-none-match", "photos/src.txt", map[string]string{"If-None-Match": "*"}, 501, "NotImplemented", ""},
		{"source sse-c", "photos/src.txt", map[string]string{"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Algorithm": "AES256"}, 501, "NotImplemented", ""},
		{"tagging directive unknown", "photos/src.txt", map[string]string{"X-Amz-Tagging-Directive": "MERGE"}, 400, "InvalidArgument", ""},
		{"tags", "photos/src.txt", map[string]string{"X-Amz-Tagging": "a=b"}, 501, "NotImplemented", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t, nil, nil)
			rec := f.copyObject(t, "dst.txt", tc.source, tc.headers)
			doc := expectError(t, rec, tc.wantStatus, tc.wantCode)
			if tc.wantKey != "" && doc.Fields["Key"] != tc.wantKey {
				t.Fatalf("Key = %q", doc.Fields["Key"])
			}
			if len(f.writer.calls) != 0 {
				t.Fatal("writer was called")
			}
		})
	}
	t.Run("tagging directive accepted", func(t *testing.T) {
		f := newCopyFixture(t, nil, nil)
		rec := f.copyObject(t, "dst.txt", "photos/src.txt", map[string]string{"X-Amz-Tagging-Directive": "REPLACE", "X-Amz-Tagging": ""})
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("version id null and encoded key", func(t *testing.T) {
		f := newCopyFixture(t, nil, nil)
		f.store.add(testBucket, "dir/a b.txt", []byte("spaced"), nil)
		rec := f.copyObject(t, "dst.txt", "photos/dir%2Fa%20b.txt?versionId=null", nil)
		if rec.Code != 200 || string(f.writer.last(t).body) != "spaced" {
			t.Fatalf("status = %d body = %q", rec.Code, f.writer.last(t).body)
		}
	})
}

func TestCopyObjectScope(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	cases := []struct {
		name    string
		buckets []string
		actions []string
	}{
		{"source bucket outside the scope", []string{testBucket}, []string{"read", "write"}},
		{"key without read", []string{"*"}, []string{"write"}},
		{"key without write", []string{"*"}, []string{"read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := "archive/kept.txt"
			if tc.name == "key without read" || tc.name == "key without write" {
				source = "photos/src.txt"
			}
			rec := f.do(t, f.handlers.CopyObject, router.OpCopyObject, "dst.txt", request{
				method: http.MethodPut, target: "/photos/dst.txt", headers: map[string]string{"X-Amz-Copy-Source": source}, auth: authFor(tc.buckets, tc.actions, 0),
			})
			expectError(t, rec, 403, "AccessDenied")
		})
	}
	if len(f.writer.calls) != 0 {
		t.Fatal("writer was called for a denied copy")
	}
}

func TestCopySourceConditions(t *testing.T) {
	lm := lastModified()
	before := httpDate(lm.Add(-time.Hour))
	after := httpDate(lm.Add(30 * time.Minute))
	cases := []struct {
		name       string
		headers    map[string]string
		wantStatus int
	}{
		{"if-match equal", map[string]string{"X-Amz-Copy-Source-If-Match": sampleETag()}, 200},
		{"if-match different", map[string]string{"X-Amz-Copy-Source-If-Match": `"other"`}, 412},
		{"if-none-match equal is 412 not 304", map[string]string{"X-Amz-Copy-Source-If-None-Match": sampleETag()}, 412},
		{"if-none-match different", map[string]string{"X-Amz-Copy-Source-If-None-Match": `"other"`}, 200},
		{"if-modified-since not modified is 412", map[string]string{"X-Amz-Copy-Source-If-Modified-Since": after}, 412},
		{"if-modified-since modified", map[string]string{"X-Amz-Copy-Source-If-Modified-Since": before}, 200},
		{"if-unmodified-since fails", map[string]string{"X-Amz-Copy-Source-If-Unmodified-Since": before}, 412},
		{"if-unmodified-since holds", map[string]string{"X-Amz-Copy-Source-If-Unmodified-Since": after}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFixture(t, nil, nil)
			rec := f.copyObject(t, "dst.txt", "photos/src.txt", tc.headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 412 {
				expectError(t, rec, 412, "PreconditionFailed")
				if len(f.writer.calls) != 0 {
					t.Fatal("writer was called")
				}
				for _, opened := range f.store.opened {
					if !opened.closed {
						t.Fatal("the source was not closed after the condition failed")
					}
				}
			}
		})
	}
}

func TestCopyFailureAfterStatusIsAnErrorDocument(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	f.writer.err = errs.ErrSlowDown
	rec := f.copyObject(t, "dst.txt", "photos/src.txt", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	declaration := `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	if !strings.HasPrefix(body, declaration) || strings.Count(body, "<?xml") != 1 {
		t.Fatalf("body = %q", body)
	}
	doc := parseError(t, strings.TrimPrefix(body, declaration))
	if doc.Code != "SlowDown" || doc.Fields["RequestId"] != testRequestID || doc.Fields["BucketName"] != testBucket {
		t.Fatalf("error = %+v", doc)
	}
	if strings.Contains(body, "<CopyObjectResult") {
		t.Fatal("a failed copy also carried a result")
	}
}

type brokenWriter struct {
	*deadlineRecorder
	writes int
}

func (b *brokenWriter) Write(p []byte) (int, error) {
	b.writes++
	if b.writes == 1 {
		return 0, errors.New("client went away")
	}
	return b.deadlineRecorder.Write(p)
}

func TestCopyDoesNotRunWhenLongResponseCannotStart(t *testing.T) {
	f := newCopyFixture(t, nil, nil)
	r := httptest.NewRequest(http.MethodPut, "/photos/dst.txt", nil)
	r.Header.Set("X-Amz-Copy-Source", "photos/src.txt")
	w := &brokenWriter{deadlineRecorder: newDeadlineRecorder()}
	f.handlers.CopyObject(w, r, routeFor(router.OpCopyObject, "dst.txt"), fullAuth(0))
	if len(f.writer.calls) != 0 {
		t.Fatal("the copy ran although the response could not start")
	}
	for _, opened := range f.store.opened {
		if !opened.closed {
			t.Fatal("the source was not closed")
		}
	}
}
