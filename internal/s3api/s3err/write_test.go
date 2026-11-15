package s3err

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type errorDocument struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	Key        string   `xml:"Key"`
	BucketName string   `xml:"BucketName"`
	Resource   string   `xml:"Resource"`
	RequestID  string   `xml:"RequestId"`
	HostID     string   `xml:"HostId"`
	Region     string   `xml:"Region"`
}

func TestErrorXMLShape(t *testing.T) {
	e := New(CodeNoSuchKey, "")
	e.BucketName = "photos"
	e.Key = "2026/cat.jpg"
	e.Extra = []Field{{Name: "Region", Value: "us-east-1"}}
	encoded := Encode(e, "4F2A9C1D7E3B5A60", "9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=")
	if strings.HasPrefix(string(encoded), "<?xml") {
		t.Fatalf("Encode returned a declaration: %s", encoded)
	}
	var doc errorDocument
	if err := xml.Unmarshal(encoded, &doc); err != nil {
		t.Fatalf("Encode output is not well-formed: %v", err)
	}
	want := errorDocument{
		XMLName:    xml.Name{Local: "Error"},
		Code:       "NoSuchKey",
		Message:    "The specified key does not exist.",
		Key:        "2026/cat.jpg",
		BucketName: "photos",
		Resource:   "/photos/2026/cat.jpg",
		RequestID:  "4F2A9C1D7E3B5A60",
		HostID:     "9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=",
		Region:     "us-east-1",
	}
	if doc != want {
		t.Fatalf("decoded %+v, want %+v", doc, want)
	}
	if strings.Contains(string(encoded), "xmlns") {
		t.Fatalf("Error root must have no namespace: %s", encoded)
	}
}

func TestErrorXMLEscapesValues(t *testing.T) {
	e := New(CodeInvalidArgument, `a<b>&"c"`)
	e.Key = "k\x00<>&"
	var doc errorDocument
	if err := xml.Unmarshal(Encode(e, "R", "H"), &doc); err != nil {
		t.Fatalf("Encode output is not well-formed: %v", err)
	}
	if doc.Message != `a<b>&"c"` {
		t.Fatalf("Message = %q", doc.Message)
	}
}

func TestWriteHeadersAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set(HeaderRequestID, "4F2A9C1D7E3B5A60")
	rec.Header().Set(HeaderHostID, "HOSTID")
	r := httptest.NewRequest(http.MethodGet, "/photos/cat.jpg", nil)
	e := New(CodeNoSuchKey, "")
	e.BucketName = "photos"
	e.Key = "cat.jpg"
	Write(rec, r, e)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	for name, want := range map[string]string{
		"Content-Type":           "application/xml",
		"X-Content-Type-Options": "nosniff",
		"Server":                 "butterdb",
		"X-Amz-Request-Id":       "4F2A9C1D7E3B5A60",
		"X-Amz-Id-2":             "HOSTID",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`+"\n<Error>") {
		t.Fatalf("body = %q", body)
	}
	var doc errorDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not well-formed: %v", err)
	}
	if doc.RequestID != rec.Header().Get(HeaderRequestID) || doc.HostID != rec.Header().Get(HeaderHostID) {
		t.Fatalf("body ids %q %q differ from headers", doc.RequestID, doc.HostID)
	}
}

func TestHeadHasNoBody(t *testing.T) {
	cases := []struct {
		name   string
		method string
		status int
		code   Code
	}{
		{"head not found", http.MethodHead, 404, CodeNoSuchBucket},
		{"head forbidden", http.MethodHead, 403, CodeAccessDenied},
		{"head slow down", http.MethodHead, 503, CodeSlowDown},
		{"get not modified", http.MethodGet, 304, CodePreconditionFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e := New(tc.code, "")
			e.Status = tc.status
			Write(rec, httptest.NewRequest(tc.method, "/photos", nil), e)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body = %q, want none", rec.Body.String())
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
		})
	}
}

func TestRetryAfterSet(t *testing.T) {
	cases := []struct {
		name string
		e    APIError
		want string
	}{
		{"whole seconds", APIError{Code: CodeSlowDown, Status: 503, RetryAfter: 17 * time.Second}, "17"},
		{"rounded up", APIError{Code: CodeSlowDown, Status: 503, RetryAfter: 1500 * time.Millisecond}, "2"},
		{"sub second is one", APIError{Code: CodeSlowDown, Status: 503, RetryAfter: time.Millisecond}, "1"},
		{"not set for other codes", New(CodeNoSuchKey, ""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Write(rec, httptest.NewRequest(http.MethodGet, "/", nil), tc.e)
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Fatalf("Retry-After = %q, want %q", got, tc.want)
			}
		})
	}
	mapped := Map(errs.ErrSlowDown)
	rec := httptest.NewRecorder()
	Write(rec, httptest.NewRequest(http.MethodGet, "/", nil), mapped)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("a mapped SlowDown carries no Retry-After")
	}
}

func TestSignatureNotEchoed(t *testing.T) {
	const providedSignature = "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	hostile := &sigv4.Error{
		Reason:           sigv4.ReasonSignature,
		Err:              wrapped(providedSignature),
		AccessKeyID:      "BDBKEXAMPLE",
		StringToSign:     "AWS4-HMAC-SHA256\n20261113T120000Z\n20261113/us-east-1/s3/aws4_request\nabc",
		CanonicalRequest: "GET\n/photos\n\nhost:x\n\nhost\nUNSIGNED-PAYLOAD",
	}
	outputs := []string{string(Encode(MapSigV4(hostile), "R", "H"))}
	for _, err := range []error{
		wrapped(providedSignature),
		&httpFailure{providedSignature},
	} {
		outputs = append(outputs, string(Encode(Map(err), "R", "H")))
	}
	for _, out := range outputs {
		if strings.Contains(out, providedSignature) {
			t.Fatalf("response echoes the provided signature: %s", out)
		}
	}
	mismatch := Map(wrappedSentinel(providedSignature))
	if mismatch.Code != CodeSignatureDoesNotMatch || strings.Contains(mismatch.Message, providedSignature) {
		t.Fatalf("Map = %+v", mismatch)
	}
}

type httpFailure struct{ text string }

func (h *httpFailure) Error() string { return "failure " + h.text }

type textError string

func (t textError) Error() string { return string(t) }

func wrapped(text string) error { return textError("signature " + text) }

type sentinelWrapper struct{ text string }

func (s sentinelWrapper) Error() string { return "mismatch for " + s.text }

func (s sentinelWrapper) Unwrap() error { return errs.ErrSignatureMismatch }

func wrappedSentinel(text string) error { return sentinelWrapper{text} }

func TestNewIDs(t *testing.T) {
	requestID, hostID, err := NewIDs(zeroReader{})
	if err != nil {
		t.Fatalf("NewIDs: %v", err)
	}
	if requestID != "0000000000000000" {
		t.Fatalf("requestID = %q", requestID)
	}
	if len(hostID) != 44 {
		t.Fatalf("hostID = %q", hostID)
	}
	if _, _, err := NewIDs(strings.NewReader("short")); err == nil {
		t.Fatal("NewIDs accepted a short random source")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestWrongRegionCodeFollowsMechanism(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   Code
	}{
		{"header authentication", "/photos/a", CodeAuthorizationHeaderMalformed},
		{"presigned query", "/photos/a?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=x", CodeAuthorizationQueryParametersError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			api := MapSigV4(&sigv4.Error{Reason: sigv4.ReasonWrongRegion, Region: "eu-west-1"})
			Write(rec, httptest.NewRequest(http.MethodGet, tc.target, nil), api)
			var doc errorDocument
			if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("body: %v", err)
			}
			if Code(doc.Code) != tc.want || rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %s status = %d, want %s", doc.Code, rec.Code, tc.want)
			}
		})
	}
}
