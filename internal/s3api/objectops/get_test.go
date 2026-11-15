package objectops

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const sampleContent = "0123456789"

func sampleETag() string {
	return `"781e5e245d69b566979b86e28d23f2c7"`
}

func lastModified() time.Time {
	return testNow.Add(-time.Hour)
}

func httpDate(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}

func newReadFixture(t *testing.T, mutate func(*object.ObjectMeta)) *fixture {
	t.Helper()
	f := newFixture(t, fixtureOptions{})
	f.store.add(testBucket, "doc.txt", []byte(sampleContent), mutate)
	return f
}

func TestRangeMatrix(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		rangeHeader  string
		wantStatus   int
		wantBody     string
		wantRange    string
		wantCode     string
		wantRequest  string
		wantActual   string
		wantUnsatisf string
	}{
		{name: "absent", content: sampleContent, wantStatus: 200, wantBody: sampleContent},
		{name: "closed range", content: sampleContent, rangeHeader: "bytes=2-5", wantStatus: 206, wantBody: "2345", wantRange: "bytes 2-5/10"},
		{name: "open ended range", content: sampleContent, rangeHeader: "bytes=7-", wantStatus: 206, wantBody: "789", wantRange: "bytes 7-9/10"},
		{name: "suffix range", content: sampleContent, rangeHeader: "bytes=-3", wantStatus: 206, wantBody: "789", wantRange: "bytes 7-9/10"},
		{name: "end past the object is clamped", content: sampleContent, rangeHeader: "bytes=5-100", wantStatus: 206, wantBody: "56789", wantRange: "bytes 5-9/10"},
		{name: "suffix larger than the object returns all as 206", content: sampleContent, rangeHeader: "bytes=-100", wantStatus: 206, wantBody: sampleContent, wantRange: "bytes 0-9/10"},
		{name: "start at the size is unsatisfiable", content: sampleContent, rangeHeader: "bytes=10-", wantStatus: 416, wantCode: "InvalidRange", wantRequest: "bytes=10-", wantActual: "10", wantUnsatisf: "bytes */10"},
		{name: "start past the size is unsatisfiable", content: sampleContent, rangeHeader: "bytes=50-60", wantStatus: 416, wantCode: "InvalidRange", wantRequest: "bytes=50-60", wantActual: "10", wantUnsatisf: "bytes */10"},
		{name: "any range on a zero byte object", content: "", rangeHeader: "bytes=0-0", wantStatus: 416, wantCode: "InvalidRange", wantRequest: "bytes=0-0", wantActual: "0", wantUnsatisf: "bytes */0"},
		{name: "multiple ranges return the full object", content: sampleContent, rangeHeader: "bytes=0-1,5-6", wantStatus: 200, wantBody: sampleContent},
		{name: "syntactically invalid range is ignored", content: sampleContent, rangeHeader: "bytes=abc", wantStatus: 200, wantBody: sampleContent},
		{name: "reversed range is ignored", content: sampleContent, rangeHeader: "bytes=5-2", wantStatus: 200, wantBody: sampleContent},
		{name: "unit other than bytes is ignored", content: sampleContent, rangeHeader: "items=0-3", wantStatus: 200, wantBody: sampleContent},
	}
	for _, tc := range cases {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				f := newFixture(t, fixtureOptions{})
				f.store.add(testBucket, "doc.txt", []byte(tc.content), nil)
				headers := map[string]string{}
				if tc.rangeHeader != "" {
					headers["Range"] = tc.rangeHeader
				}
				var rec *deadlineRecorder
				if method == http.MethodGet {
					rec = f.get(t, "doc.txt", headers, fullAuth(0), "")
				} else {
					rec = f.head(t, "doc.txt", headers, "")
				}
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if rec.Header().Get("Accept-Ranges") != "bytes" && tc.wantStatus != 416 {
					t.Fatalf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
				}
				if tc.wantStatus == 416 {
					if method == http.MethodGet {
						doc := parseError(t, rec.Body.String())
						if doc.Code != tc.wantCode || doc.Fields["RangeRequested"] != tc.wantRequest || doc.Fields["ActualObjectSize"] != tc.wantActual {
							t.Fatalf("error = %+v", doc)
						}
					} else if rec.Body.Len() != 0 {
						t.Fatalf("HEAD error carried a body: %q", rec.Body.String())
					}
					if got := rec.Header().Get("Content-Range"); got != tc.wantUnsatisf {
						t.Fatalf("Content-Range = %q, want %q", got, tc.wantUnsatisf)
					}
					return
				}
				if got := rec.Header().Get("Content-Range"); got != tc.wantRange {
					t.Fatalf("Content-Range = %q, want %q", got, tc.wantRange)
				}
				if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(tc.wantBody)) {
					t.Fatalf("Content-Length = %q, want %d", got, len(tc.wantBody))
				}
				if method == http.MethodGet && rec.Body.String() != tc.wantBody {
					t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
				}
				if method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatalf("HEAD carried a body: %q", rec.Body.String())
				}
			})
		}
	}
}

func TestConditionalMatrix(t *testing.T) {
	lm := lastModified()
	before := httpDate(lm.Add(-time.Hour))
	after := httpDate(lm.Add(30 * time.Minute))
	future := httpDate(testNow.Add(24 * time.Hour))
	cases := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{"no conditions", nil, 200, ""},
		{"if-match equal", map[string]string{"If-Match": sampleETag()}, 200, ""},
		{"if-match equal without quotes", map[string]string{"If-Match": "781e5e245d69b566979b86e28d23f2c7"}, 200, ""},
		{"if-match star", map[string]string{"If-Match": "*"}, 200, ""},
		{"if-match different", map[string]string{"If-Match": `"other"`}, 412, "PreconditionFailed"},
		{"if-match weak validator never matches", map[string]string{"If-Match": `W/` + sampleETag()}, 412, "PreconditionFailed"},
		{"if-unmodified-since after the modification", map[string]string{"If-Unmodified-Since": after}, 200, ""},
		{"if-unmodified-since before the modification", map[string]string{"If-Unmodified-Since": before}, 412, "PreconditionFailed"},
		{"if-match true skips if-unmodified-since", map[string]string{"If-Match": sampleETag(), "If-Unmodified-Since": before}, 200, ""},
		{"if-match false wins over if-unmodified-since true", map[string]string{"If-Match": `"other"`, "If-Unmodified-Since": after}, 412, "PreconditionFailed"},
		{"if-unmodified-since in the future is ignored", map[string]string{"If-Unmodified-Since": future}, 200, ""},
		{"if-unmodified-since unparseable is ignored", map[string]string{"If-Unmodified-Since": "yesterday"}, 200, ""},
		{"if-none-match equal", map[string]string{"If-None-Match": sampleETag()}, 304, ""},
		{"if-none-match star", map[string]string{"If-None-Match": "*"}, 304, ""},
		{"if-none-match weak equal", map[string]string{"If-None-Match": `W/` + sampleETag()}, 304, ""},
		{"if-none-match different", map[string]string{"If-None-Match": `"other"`}, 200, ""},
		{"if-modified-since not modified", map[string]string{"If-Modified-Since": after}, 304, ""},
		{"if-modified-since modified", map[string]string{"If-Modified-Since": before}, 200, ""},
		{"if-none-match different skips if-modified-since", map[string]string{"If-None-Match": `"other"`, "If-Modified-Since": after}, 200, ""},
		{"if-none-match equal wins over if-modified-since", map[string]string{"If-None-Match": sampleETag(), "If-Modified-Since": before}, 304, ""},
	}
	for _, tc := range cases {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				f := newReadFixture(t, func(m *object.ObjectMeta) {
					m.SystemMeta = map[string]string{"expires": "Wed, 21 Oct 2037 07:28:00 GMT", "cache-control": "max-age=60"}
				})
				var rec *deadlineRecorder
				if method == http.MethodGet {
					rec = f.get(t, "doc.txt", tc.headers, fullAuth(0), "")
				} else {
					rec = f.head(t, "doc.txt", tc.headers, "")
				}
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
				}
				switch tc.wantStatus {
				case 304:
					if rec.Body.Len() != 0 {
						t.Fatalf("304 carried a body: %q", rec.Body.String())
					}
					for header, want := range map[string]string{"ETag": sampleETag(), "Last-Modified": httpDate(lm), "Cache-Control": "max-age=60", "Expires": "Wed, 21 Oct 2037 07:28:00 GMT"} {
						if got := rec.Header().Get(header); got != want {
							t.Fatalf("%s = %q, want %q", header, got, want)
						}
					}
				case 412:
					if method == http.MethodGet {
						doc := parseError(t, rec.Body.String())
						if doc.Code != tc.wantCode || doc.Fields["Condition"] == "" {
							t.Fatalf("error = %+v", doc)
						}
					}
				}
			})
		}
	}
}

func TestIfRange(t *testing.T) {
	lm := httpDate(lastModified())
	other := httpDate(lastModified().Add(-time.Minute))
	cases := []struct {
		name       string
		ifRange    string
		wantStatus int
		wantBody   string
	}{
		{"matching strong etag honours the range", sampleETag(), 206, "2345"},
		{"unquoted etag is accepted", "781e5e245d69b566979b86e28d23f2c7", 206, "2345"},
		{"different etag ignores the range", `"other"`, 200, sampleContent},
		{"weak etag ignores the range", `W/` + sampleETag(), 200, sampleContent},
		{"equal date honours the range", lm, 206, "2345"},
		{"different date ignores the range", other, 200, sampleContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t, nil)
			rec := f.get(t, "doc.txt", map[string]string{"Range": "bytes=2-5", "If-Range": tc.ifRange}, fullAuth(0), "")
			if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
				t.Fatalf("status = %d body = %q, want %d %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestResponseOverridesSignedOnly(t *testing.T) {
	publicRead := map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPublicRead, DefaultEncryption: bucket.EncryptionNone}}
	query := "?response-content-type=image%2Fpng&response-content-language=de&response-expires=Thu%2C%2001%20Dec%202044%2016%3A00%3A00%20GMT&response-cache-control=no-store&response-content-disposition=attachment%3B%20filename%3D%22x.png%22&response-content-encoding=gzip"
	t.Run("signed request applies every override on 200 and 206", func(t *testing.T) {
		f := newReadFixture(t, nil)
		for _, rangeHeader := range []string{"", "bytes=0-3"} {
			headers := map[string]string{}
			if rangeHeader != "" {
				headers["Range"] = rangeHeader
			}
			rec := f.get(t, "doc.txt", headers, fullAuth(0), query)
			want := map[string]string{
				"Content-Type":        "image/png",
				"Content-Language":    "de",
				"Expires":             "Thu, 01 Dec 2044 16:00:00 GMT",
				"Cache-Control":       "no-store",
				"Content-Disposition": `attachment; filename="x.png"`,
				"Content-Encoding":    "gzip",
			}
			for header, value := range want {
				if got := rec.Header().Get(header); got != value {
					t.Fatalf("range %q: %s = %q, want %q", rangeHeader, header, got, value)
				}
			}
		}
	})
	t.Run("anonymous request is refused", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{infos: publicRead})
		f.store.add(testBucket, "doc.txt", []byte(sampleContent), nil)
		rec := f.get(t, "doc.txt", nil, anonymousAuth(), "?response-content-type=text%2Fhtml")
		doc := expectError(t, rec, 400, "InvalidRequest")
		if doc.Message != "Request specific response headers cannot be used for anonymous GET requests." {
			t.Fatalf("message = %q", doc.Message)
		}
	})
	t.Run("anonymous request without overrides is served", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{infos: publicRead})
		f.store.add(testBucket, "doc.txt", []byte(sampleContent), nil)
		rec := f.get(t, "doc.txt", nil, anonymousAuth(), "")
		if rec.Code != 200 || rec.Body.String() != sampleContent {
			t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("control character and length are refused", func(t *testing.T) {
		f := newReadFixture(t, nil)
		expectError(t, f.get(t, "doc.txt", nil, fullAuth(0), "?response-content-type=a%0Ab"), 400, "InvalidArgument")
		expectError(t, f.get(t, "doc.txt", nil, fullAuth(0), "?response-content-type="+strings.Repeat("a", 1025)), 400, "InvalidArgument")
	})
	t.Run("overrides apply to head", func(t *testing.T) {
		f := newReadFixture(t, nil)
		rec := f.head(t, "doc.txt", nil, "?response-content-type=image%2Fgif")
		if rec.Header().Get("Content-Type") != "image/gif" {
			t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
		}
	})
}

func TestPartNumberOne(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{"part one returns the whole object as 206", "?partNumber=1", nil, 206, ""},
		{"part two is not satisfiable", "?partNumber=2", nil, 416, "InvalidPartNumber"},
		{"part zero is not satisfiable", "?partNumber=0", nil, 416, "InvalidPartNumber"},
		{"non numeric part", "?partNumber=x", nil, 416, "InvalidPartNumber"},
		{"part number with range", "?partNumber=1", map[string]string{"Range": "bytes=0-1"}, 400, "InvalidRequest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t, nil)
			rec := f.get(t, "doc.txt", tc.headers, fullAuth(0), tc.query)
			if tc.wantCode != "" {
				expectError(t, rec, tc.wantStatus, tc.wantCode)
				return
			}
			if rec.Code != 206 || rec.Body.String() != sampleContent || rec.Header().Get("Content-Range") != "bytes 0-9/10" {
				t.Fatalf("status = %d range = %q body = %q", rec.Code, rec.Header().Get("Content-Range"), rec.Body.String())
			}
			if rec.Header().Get("X-Amz-Mp-Parts-Count") != "" {
				t.Fatal("parts count header was sent")
			}
		})
	}
}

func TestGetResponseHeaders(t *testing.T) {
	f := newReadFixture(t, func(m *object.ObjectMeta) {
		m.Encrypted = true
		m.ContentType = "image/png"
		m.UserMeta = map[string]string{"color": "blue"}
		m.SystemMeta = map[string]string{"content-disposition": "inline", "content-language": "en", "content-encoding": "gzip", "expires": "Wed, 21 Oct 2037 07:28:00 GMT"}
	})
	rec := f.get(t, "doc.txt", nil, fullAuth(0), "")
	want := map[string]string{
		"Content-Type":                 "image/png",
		"Content-Length":               "10",
		"ETag":                         sampleETag(),
		"Last-Modified":                httpDate(lastModified()),
		"Accept-Ranges":                "bytes",
		"X-Amz-Meta-Color":             "blue",
		"X-Amz-Server-Side-Encryption": "AES256",
		"Content-Disposition":          "inline",
		"Content-Language":             "en",
		"Content-Encoding":             "gzip",
		"Expires":                      "Wed, 21 Oct 2037 07:28:00 GMT",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Fatalf("%s = %q, want %q", header, got, value)
		}
	}
	for _, header := range []string{"X-Amz-Version-Id", "X-Amz-Storage-Class", "X-Amz-Tagging-Count", "X-Amz-Checksum-Crc32", "Cache-Control"} {
		if _, present := rec.Header()[header]; present {
			t.Fatalf("%s must not be sent", header)
		}
	}
}

func TestPlaintextObjectHasNoEncryptionHeader(t *testing.T) {
	f := newReadFixture(t, func(m *object.ObjectMeta) { m.ContentType = "" })
	rec := f.get(t, "doc.txt", nil, fullAuth(0), "")
	if _, present := rec.Header()["X-Amz-Server-Side-Encryption"]; present {
		t.Fatal("plaintext object carried the encryption header")
	}
	if rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestCacheControlPrecedence(t *testing.T) {
	publicRead := func(maxAge int64) map[bucket.BucketName]bucket.Info {
		return map[bucket.BucketName]bucket.Info{testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPublicRead, DefaultEncryption: bucket.EncryptionNone, CacheMaxAge: maxAge}}
	}
	cases := []struct {
		name  string
		infos map[bucket.BucketName]bucket.Info
		store string
		query string
		want  string
	}{
		{"override wins", publicRead(3600), "max-age=5", "?response-cache-control=no-store", "no-store"},
		{"stored wins over bucket", publicRead(3600), "max-age=5", "", "max-age=5"},
		{"public bucket default", publicRead(3600), "", "", "public, max-age=3600"},
		{"public bucket without max age", publicRead(0), "", "", "no-cache"},
		{"private bucket without stored value", nil, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: tc.infos})
			f.store.add(testBucket, "doc.txt", []byte(sampleContent), func(m *object.ObjectMeta) {
				if tc.store != "" {
					m.SystemMeta = map[string]string{"cache-control": tc.store}
				}
			})
			rec := f.get(t, "doc.txt", nil, fullAuth(0), tc.query)
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetMissingKey(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	doc := expectError(t, f.get(t, "missing.txt", nil, fullAuth(0), ""), 404, "NoSuchKey")
	if doc.Fields["Key"] != "missing.txt" || doc.Fields["BucketName"] != testBucket {
		t.Fatalf("fields = %v", doc.Fields)
	}
	head := f.head(t, "missing.txt", nil, "")
	if head.Code != 404 || head.Body.Len() != 0 {
		t.Fatalf("head status = %d body = %q", head.Code, head.Body.String())
	}
}

func TestHeadUsesMetadataOnly(t *testing.T) {
	f := newReadFixture(t, nil)
	rec := f.head(t, "doc.txt", nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Length") != "10" || rec.Body.Len() != 0 {
		t.Fatalf("status = %d length = %q body = %d bytes", rec.Code, rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	if len(f.store.opens) != 0 || len(f.store.heads) != 1 {
		t.Fatalf("opens = %v heads = %v", f.store.opens, f.store.heads)
	}
}

func TestGetClosesOpenedObjectOnEveryPath(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		query   string
	}{
		{"full", nil, ""},
		{"range", map[string]string{"Range": "bytes=1-2"}, ""},
		{"not modified", map[string]string{"If-None-Match": "*"}, ""},
		{"precondition failed", map[string]string{"If-Match": `"x"`}, ""},
		{"unsatisfiable", map[string]string{"Range": "bytes=99-"}, ""},
		{"bad part", nil, "?partNumber=9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t, nil)
			f.get(t, "doc.txt", tc.headers, fullAuth(0), tc.query)
			f.get(t, "doc.txt", tc.headers, fullAuth(0), tc.query)
			if len(f.store.opened) == 0 {
				return
			}
			for i, opened := range f.store.opened {
				if !opened.closed {
					t.Fatalf("opened object %d was not closed", i)
				}
			}
			if len(f.store.opened) != len(f.store.opens) {
				t.Fatalf("every request must open its own object: opened %d opens %d", len(f.store.opened), len(f.store.opens))
			}
		})
	}
}

func TestGetAccess(t *testing.T) {
	f := newReadFixture(t, nil)
	cases := []struct {
		name string
		auth func() (bool, []string, []string)
		code string
		stat int
	}{
		{"write only key", func() (bool, []string, []string) { return false, []string{"*"}, []string{"write"} }, "AccessDenied", 403},
		{"anonymous on a private bucket", func() (bool, []string, []string) { return true, nil, nil }, "AccessDenied", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anonymous, buckets, actions := tc.auth()
			auth := anonymousAuth()
			if !anonymous {
				auth = authFor(buckets, actions, 0)
			}
			expectError(t, f.get(t, "doc.txt", nil, auth, ""), tc.stat, tc.code)
		})
	}
	if len(f.store.opens) != 0 {
		t.Fatalf("denied requests opened objects: %v", f.store.opens)
	}
}

func TestGetUnsupportedFeatures(t *testing.T) {
	f := newReadFixture(t, nil)
	expectError(t, f.get(t, "doc.txt", map[string]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm": "AES256"}, fullAuth(0), ""), 501, "NotImplemented")
	expectError(t, f.get(t, "doc.txt", nil, fullAuth(0), "?versionId=abc"), 400, "InvalidArgument")
	if rec := f.get(t, "doc.txt", nil, fullAuth(0), "?versionId=null"); rec.Code != 200 {
		t.Fatalf("versionId=null status = %d", rec.Code)
	}
}

func TestGetReadFailures(t *testing.T) {
	t.Run("failure before the first byte is a 503", func(t *testing.T) {
		f := newReadFixture(t, nil)
		f.store.failure = errs.ErrSlowDown
		f.store.cut = 0
		rec := f.get(t, "doc.txt", nil, fullAuth(0), "")
		expectError(t, rec, 503, "SlowDown")
		if rec.Header().Get("Retry-After") == "" {
			t.Fatal("Retry-After missing")
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Length") != "" && rec.Header().Get("Content-Type") != "application/xml" {
			t.Fatalf("object headers leaked into the error response: %v", rec.Header())
		}
	})
	t.Run("non transient failure before headers is a 500", func(t *testing.T) {
		f := newReadFixture(t, nil)
		f.store.failure = errors.New("telegram unreachable")
		f.store.cut = 0
		expectError(t, f.get(t, "doc.txt", nil, fullAuth(0), ""), 500, "InternalError")
	})
	t.Run("failure after the first byte aborts the connection", func(t *testing.T) {
		f := newReadFixture(t, nil)
		f.store.failure = io.ErrUnexpectedEOF
		f.store.cut = 4
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			f.get(t, "doc.txt", nil, fullAuth(0), "")
		}()
		if recovered != http.ErrAbortHandler {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
		}
	})
}
