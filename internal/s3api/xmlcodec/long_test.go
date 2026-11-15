package xmlcodec

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type streamWriter struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	buf     bytes.Buffer
	flushes chan struct{}
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: http.Header{}, flushes: make(chan struct{}, 1024)}
}

func (s *streamWriter) Header() http.Header { return s.header }

func (s *streamWriter) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *streamWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(b)
}

func (s *streamWriter) Flush() { s.flushes <- struct{}{} }

func (s *streamWriter) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *streamWriter) waitFlushes(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-s.flushes:
		case <-time.After(5 * time.Second):
			t.Fatalf("flush %d of %d never happened", i+1, n)
		}
	}
}

type completeResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	Bucket  string   `xml:"Bucket"`
	ETag    string   `xml:"ETag"`
}

func TestLongResponseDeclarationFirst(t *testing.T) {
	w := newStreamWriter()
	long, err := StartLongResponse(w, time.Hour)
	if err != nil {
		t.Fatalf("StartLongResponse: %v", err)
	}
	w.waitFlushes(t, 1)
	if got := w.body(); got != xmlDeclaration {
		t.Fatalf("body before any keepalive = %q, want the declaration only", got)
	}
	if w.status != http.StatusOK {
		t.Fatalf("status = %d", w.status)
	}
	if got := w.header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want none", got)
	}
	if got := w.header.Get("Content-Type"); got != "application/xml" {
		t.Fatalf("Content-Type = %q", got)
	}
	if err := long.Succeed(completeResult{Bucket: "photos", ETag: "x"}); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	if !strings.HasPrefix(w.body(), `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Fatalf("document does not start with the declaration: %q", w.body())
	}
}

func TestLongResponseKeepalive(t *testing.T) {
	w := newStreamWriter()
	long, err := StartLongResponse(w, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("StartLongResponse: %v", err)
	}
	w.waitFlushes(t, 4)
	if err := long.Succeed(completeResult{Bucket: "photos", ETag: "x"}); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	body := w.body()
	rest := strings.TrimPrefix(body, xmlDeclaration)
	spaces := len(rest) - len(strings.TrimLeft(rest, " "))
	if spaces < 3 {
		t.Fatalf("keepalive spaces = %d, want at least 3 in %q", spaces, body)
	}
	if !strings.Contains(rest, `xmlns="`+Namespace+`"`) {
		t.Fatalf("root element missing namespace: %q", rest)
	}
	var parsed completeResult
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("final document is not well-formed: %v\n%s", err, body)
	}
	if parsed.Bucket != "photos" {
		t.Fatalf("parsed %+v", parsed)
	}
}

func TestLongResponseFailIsWellFormed(t *testing.T) {
	w := newStreamWriter()
	long, err := StartLongResponse(w, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("StartLongResponse: %v", err)
	}
	w.waitFlushes(t, 3)
	errorXML := []byte(`<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`)
	if err := long.Fail(errorXML); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	body := w.body()
	if !strings.HasSuffix(body, string(errorXML)) {
		t.Fatalf("body does not end with the error document: %q", body)
	}
	var parsed struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("document is not well-formed: %v", err)
	}
	if parsed.Code != "SlowDown" {
		t.Fatalf("Code = %q", parsed.Code)
	}
}

func TestLongResponseNoKeepaliveAfterFinish(t *testing.T) {
	w := newStreamWriter()
	long, err := StartLongResponse(w, time.Millisecond)
	if err != nil {
		t.Fatalf("StartLongResponse: %v", err)
	}
	if err := long.Succeed(completeResult{Bucket: "b"}); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	finished := w.body()
	time.Sleep(20 * time.Millisecond)
	if got := w.body(); got != finished {
		t.Fatalf("bytes written after Succeed: %q", got[len(finished):])
	}
}

func TestLongResponseRejectsNonPositiveInterval(t *testing.T) {
	if _, err := StartLongResponse(newStreamWriter(), 0); err == nil {
		t.Fatal("StartLongResponse accepted a zero interval")
	}
}
