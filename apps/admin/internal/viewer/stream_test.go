package viewer

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type memorySource struct {
	mu     sync.Mutex
	meta   adminv1.ObjectMeta
	data   []byte
	opts   []adminv1.GetObjectOptions
	keys   []string
	err    error
	metaErr error
	closed bool
}

func (s *memorySource) ObjectMeta(context.Context, string, string) (adminv1.ObjectMeta, error) {
	return s.meta, s.metaErr
}

func (s *memorySource) GetObject(_ context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts = append(s.opts, opts)
	s.keys = append(s.keys, bucket+"/"+key)
	if s.err != nil {
		return adminv1.ObjectStream{}, s.err
	}
	data := s.data
	status := http.StatusOK
	if opts.RangeStart != 0 || opts.RangeEnd != 0 {
		end := int64(len(data))
		if opts.RangeEnd != 0 {
			end = opts.RangeEnd + 1
		}
		data = data[opts.RangeStart:end]
		status = http.StatusPartialContent
	}
	return adminv1.ObjectStream{Body: &trackedBody{Reader: bytes.NewReader(data), close: func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}}, Status: status, ContentLength: int64(len(data))}, nil
}

type trackedBody struct {
	io.Reader
	close func()
}

func (b *trackedBody) Close() error { b.close(); return nil }

func TestMediaStreamRanges(t *testing.T) {
	cases := []struct {
		name, method, header, body, contentRange string
		status                                 int
	}{
		{"full", "GET", "", "0123456789", "", 200},
		{"bounded", "GET", "bytes=2-5", "2345", "bytes 2-5/10", 206},
		{"first byte", "GET", "bytes=0-0", "0", "bytes 0-0/10", 206},
		{"open", "GET", "bytes=6-", "6789", "bytes 6-9/10", 206},
		{"suffix", "GET", "bytes=-3", "789", "bytes 7-9/10", 206},
		{"clamped", "GET", "bytes=8-99", "89", "bytes 8-9/10", 206},
		{"head", "HEAD", "", "", "", 200},
		{"head ignores range", "HEAD", "bytes=2-5", "", "", 200},
		{"beyond", "GET", "bytes=10-", "", "bytes */10", 416},
		{"reversed", "GET", "bytes=5-2", "", "bytes */10", 416},
		{"zero suffix", "GET", "bytes=-0", "", "bytes */10", 416},
		{"multiple", "GET", "bytes=0-1,4-5", "", "bytes */10", 416},
		{"malformed", "GET", "bytes=nope-", "", "bytes */10", 416},
		{"wrong method", "POST", "", "", "", 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &memorySource{meta: adminv1.ObjectMeta{Key: "folder/movie ? #.mp4", ContentType: "video/mp4", SizeBytes: 10, ETag: `"v1"`}, data: []byte("0123456789")}
			stream, err := newStream(context.Background(), source, "photos", source.meta, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			req, _ := http.NewRequest(tc.method, stream.URL, nil)
			req.Header.Set("Range", tc.header)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.status || resp.Header.Get("Content-Range") != tc.contentRange {
				t.Fatalf("response = %d %q, want %d %q", resp.StatusCode, resp.Header.Get("Content-Range"), tc.status, tc.contentRange)
			}
			if tc.status < 300 && string(body) != tc.body {
				t.Fatalf("body = %q, want %q", body, tc.body)
			}
			source.mu.Lock()
			defer source.mu.Unlock()
			if tc.method == "GET" && tc.status < 300 {
				if len(source.opts) != 1 || source.opts[0].IfMatch != `"v1"` || source.keys[0] != "photos/folder/movie ? #.mp4" || !source.closed {
					t.Fatalf("upstream requests = %+v, keys %v, closed %v", source.opts, source.keys, source.closed)
				}
			} else if len(source.opts) != 0 {
				t.Fatal("request unexpectedly read the object")
			}
		})
	}
}

func TestMediaStreamRejectsUnknownPathsAndChangedObjects(t *testing.T) {
	source := &memorySource{meta: adminv1.ObjectMeta{Key: "movie.mp4", SizeBytes: 10}, err: &adminv1.APIError{Status: 412}}
	stream, err := newStream(context.Background(), source, "photos", source.meta, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for _, tc := range []struct {
		url    string
		status int
	}{{stream.URL + "/other", 404}, {stream.URL, 412}} {
		resp, err := http.Get(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
		}
	}
}

func TestMediaStreamCancellationClosesListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &memorySource{}
	stream, err := newStream(ctx, source, "photos", source.meta, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.Get(stream.URL); err == nil {
		resp.Body.Close()
		t.Fatal("stream still accepts requests after closing")
	}
}

func TestMediaStreamEmptyObject(t *testing.T) {
	source := &memorySource{meta: adminv1.ObjectMeta{Key: "empty"}}
	stream, err := newStream(context.Background(), source, "photos", source.meta, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	resp, err := http.Get(stream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) != 0 || resp.StatusCode != 200 {
		t.Fatalf("empty response: %q, %v, status %d", data, err, resp.StatusCode)
	}
}

func TestMediaStreamRandomFailure(t *testing.T) {
	_, err := newStream(context.Background(), &memorySource{}, "photos", adminv1.ObjectMeta{}, strings.NewReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("random error = %v, want EOF", err)
	}
}
