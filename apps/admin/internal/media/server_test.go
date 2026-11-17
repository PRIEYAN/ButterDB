package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type fakeSource struct {
	mu     sync.Mutex
	meta   map[string]adminv1.ObjectMeta
	data   map[string][]byte
	opens  []adminv1.GetObjectOptions
	open   int
	closed int
}

func newFakeSource() *fakeSource {
	return &fakeSource{meta: map[string]adminv1.ObjectMeta{}, data: map[string][]byte{}}
}

func (f *fakeSource) put(key, contentType string, body []byte) {
	f.meta[key] = adminv1.ObjectMeta{Key: key, ContentType: contentType, SizeBytes: int64(len(body)), ETag: `"v1"`}
	f.data[key] = body
}

func (f *fakeSource) ObjectMeta(_ context.Context, _, key string) (adminv1.ObjectMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	meta, ok := f.meta[key]
	if !ok {
		return adminv1.ObjectMeta{}, &adminv1.APIError{Status: http.StatusNotFound}
	}
	return meta, nil
}

type trackedBody struct {
	io.Reader
	f *fakeSource
}

func (b trackedBody) Close() error {
	b.f.mu.Lock()
	b.f.closed++
	b.f.open--
	b.f.mu.Unlock()
	return nil
}

func (f *fakeSource) GetObject(_ context.Context, _, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, opts)
	if opts.IfMatch != "" && opts.IfMatch != f.meta[key].ETag {
		return adminv1.ObjectStream{}, &adminv1.APIError{Status: http.StatusPreconditionFailed}
	}
	body := f.data[key]
	status := http.StatusOK
	if opts.RangeStart != 0 || opts.RangeEnd != 0 {
		body = body[opts.RangeStart : opts.RangeEnd+1]
		status = http.StatusPartialContent
	}
	f.open++
	return adminv1.ObjectStream{Body: trackedBody{Reader: bytes.NewReader(body), f: f}, Status: status, ContentLength: int64(len(body))}, nil
}

func testBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func newTestService(t *testing.T, key, contentType string, body []byte) (*Service, *fakeSource) {
	t.Helper()
	src := newFakeSource()
	src.put(key, contentType, body)
	svc := NewService(context.Background(), src)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, src
}

func TestMediaServerServesTokenizedRanges(t *testing.T) {
	body := testBytes(2048)
	svc, _ := newTestService(t, "movies/clip.mp4", "video/mp4", body)

	opened, err := svc.Open(context.Background(), "movies", "movies/clip.mp4")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.URL == "" || opened.Token == "" || opened.Kind != StreamKindVideo || opened.Name != "clip.mp4" {
		t.Fatalf("open result = %+v", opened)
	}

	req, _ := http.NewRequest(http.MethodGet, opened.URL, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes 100-199/2048" {
		t.Fatalf("Content-Range = %q", resp.Header.Get("Content-Range"))
	}
	if !bytes.Equal(got, body[100:200]) {
		t.Fatal("range body mismatch")
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}

	if !strings.HasSuffix(opened.URL, "/"+opened.Token+"/clip.mp4") {
		t.Fatalf("URL %q should end with the token and the file name", opened.URL)
	}
	badURL := strings.Replace(opened.URL, opened.Token, "bad-token", 1)
	resp, err = http.Get(badURL)
	if err != nil {
		t.Fatalf("bad token GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad token status = %d, want 404", resp.StatusCode)
	}
}

func TestMediaServerRangeForms(t *testing.T) {
	body := testBytes(512)
	svc, _ := newTestService(t, "clip.mp4", "video/mp4", body)
	opened, err := svc.Open(context.Background(), "b", "clip.mp4")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	tests := []struct {
		header string
		want   []byte
		crange string
	}{
		{header: "bytes=100-", want: body[100:], crange: "bytes 100-511/512"},
		{header: "bytes=-16", want: body[496:], crange: "bytes 496-511/512"},
		{header: "bytes=0-0", want: body[:1], crange: "bytes 0-0/512"},
	}
	for _, tt := range tests {
		req, _ := http.NewRequest(http.MethodGet, opened.URL, nil)
		req.Header.Set("Range", tt.header)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tt.header, err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != tt.crange {
			t.Fatalf("%s status/range = %d %q", tt.header, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
		if !bytes.Equal(got, tt.want) {
			t.Fatalf("%s body mismatch", tt.header)
		}
	}

	resp, err := http.Get(opened.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("full read status %d, %d bytes", resp.StatusCode, len(got))
	}

	req, _ := http.NewRequest(http.MethodGet, opened.URL, nil)
	req.Header.Set("Range", "bytes=999-1000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("invalid range: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status = %d, want 416", resp.StatusCode)
	}
}

func TestMediaServerClosesSessionAndReader(t *testing.T) {
	svc, src := newTestService(t, "clip.mp4", "video/mp4", testBytes(256))
	opened, err := svc.Open(context.Background(), "b", "clip.mp4")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resp, err := http.Get(opened.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err := svc.CloseSession(opened.Token); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	resp, err = http.Get(opened.URL)
	if err != nil {
		t.Fatalf("GET after close: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status after close = %d, want 404", resp.StatusCode)
	}
	if err := svc.CloseSession(opened.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second close err = %v, want ErrSessionNotFound", err)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.open != 0 {
		t.Fatalf("%d upstream reads left open after close", src.open)
	}
}

func TestMediaSessionStreamsSequentiallyAndReopensOnSeek(t *testing.T) {
	body := testBytes(10_000)
	svc, src := newTestService(t, "song.mp3", "audio/mpeg", body)
	opened, err := svc.Open(context.Background(), "b", "song.mp3")
	if err != nil {
		t.Fatal(err)
	}
	session := svc.server.session(opened.Token)
	buf := make([]byte, 1000)
	for off := int64(0); off < 3000; off += 1000 {
		n, err := session.ReadAt(context.Background(), buf, off)
		if err != nil || n != 1000 || !bytes.Equal(buf, body[off:off+1000]) {
			t.Fatalf("sequential read at %d = %d, %v", off, n, err)
		}
	}
	n, err := session.ReadAt(context.Background(), buf, 8000)
	if err != nil || n != 1000 || !bytes.Equal(buf, body[8000:9000]) {
		t.Fatalf("seek read = %d, %v", n, err)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.opens) != 2 || src.opens[0].RangeStart != 0 || src.opens[1].RangeStart != 8000 || src.opens[1].RangeEnd != 9999 {
		t.Fatalf("upstream opens = %+v, want one stream plus one reopen at the seek", src.opens)
	}
	for _, o := range src.opens {
		if o.IfMatch != `"v1"` {
			t.Fatalf("read not pinned to the opened version: %+v", o)
		}
	}
}

func TestMediaSessionRejectsChangedObject(t *testing.T) {
	svc, src := newTestService(t, "clip.mp4", "video/mp4", testBytes(100))
	opened, err := svc.Open(context.Background(), "b", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	meta := src.meta["clip.mp4"]
	meta.ETag = `"v2"`
	src.meta["clip.mp4"] = meta
	src.mu.Unlock()
	_, err = svc.server.session(opened.Token).ReadAt(context.Background(), make([]byte, 10), 0)
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("read after change err = %v, want ErrStaleRevision", err)
	}
}

func TestMediaOpenRejectsUnsupportedAndMissingFiles(t *testing.T) {
	src := newFakeSource()
	src.put("notes.pdf", "application/pdf", testBytes(10))
	src.put("raw", "audio/flac", testBytes(10))
	svc := NewService(context.Background(), src)
	defer svc.Close()
	if _, err := svc.Open(context.Background(), "b", "notes.pdf"); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("pdf err = %v", err)
	}
	if _, err := svc.Open(context.Background(), "b", "missing.mp4"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	opened, err := svc.Open(context.Background(), "b", "raw")
	if err != nil || opened.Kind != StreamKindAudio || opened.MimeType != "audio/flac" {
		t.Fatalf("extensionless audio = %+v, %v", opened, err)
	}
}

func TestParseRangeHeader(t *testing.T) {
	tests := []struct {
		raw     string
		size    int64
		start   int64
		end     int64
		partial bool
		ok      bool
	}{
		{raw: "", size: 10, partial: false, ok: true},
		{raw: "bytes=2-5", size: 10, start: 2, end: 5, partial: true, ok: true},
		{raw: "bytes=2-", size: 10, start: 2, end: 9, partial: true, ok: true},
		{raw: "bytes=-3", size: 10, start: 7, end: 9, partial: true, ok: true},
		{raw: "bytes=8-99", size: 10, start: 8, end: 9, partial: true, ok: true},
		{raw: "bytes=10-11", size: 10, partial: true, ok: false},
		{raw: "bytes=4-2", size: 10, partial: true, ok: false},
		{raw: "bytes=1-2,3-4", size: 10, partial: true, ok: false},
	}
	for _, tt := range tests {
		start, end, partial, ok := parseRangeHeader(tt.raw, tt.size)
		if start != tt.start || end != tt.end || partial != tt.partial || ok != tt.ok {
			t.Fatalf("%q = %d,%d,%v,%v want %d,%d,%v,%v", tt.raw, start, end, partial, ok, tt.start, tt.end, tt.partial, tt.ok)
		}
	}
}

func TestContentTypeForCommonVideoContainers(t *testing.T) {
	tests := map[string]string{
		"clip.mp4":  "video/mp4",
		"clip.mov":  "video/quicktime",
		"clip.webm": "video/webm",
		"clip.mkv":  "video/x-matroska",
		"clip.avi":  "video/x-msvideo",
		"clip.ts":   "video/mp2t",
	}
	for name, want := range tests {
		if got := contentTypeFor(name); got != want {
			t.Fatalf("contentTypeFor(%q) = %q, want %q", name, got, want)
		}
	}
}
