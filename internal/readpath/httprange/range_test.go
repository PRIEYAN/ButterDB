package httprange

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func TestParseRangeHeader(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		size    int64
		start   int64
		end     int64
		partial bool
		invalid bool
	}{
		{name: "absent", raw: "", size: 10},
		{name: "closed", raw: "bytes=2-5", size: 10, start: 2, end: 5, partial: true},
		{name: "open ended", raw: "bytes=2-", size: 10, start: 2, end: 9, partial: true},
		{name: "suffix", raw: "bytes=-3", size: 10, start: 7, end: 9, partial: true},
		{name: "end clamped", raw: "bytes=8-99", size: 10, start: 8, end: 9, partial: true},
		{name: "suffix larger than object", raw: "bytes=-99", size: 10, start: 0, end: 9, partial: true},
		{name: "first byte only", raw: "bytes=0-0", size: 10, start: 0, end: 0, partial: true},
		{name: "unit is case insensitive", raw: "Bytes=1-2", size: 10, start: 1, end: 2, partial: true},
		{name: "spaces around spec", raw: " bytes= 1-2 ", size: 10, start: 1, end: 2, partial: true},
		{name: "start at size", raw: "bytes=10-11", size: 10, invalid: true},
		{name: "open start at size", raw: "bytes=10-", size: 10, invalid: true},
		{name: "empty suffix", raw: "bytes=-0", size: 10, invalid: true},
		{name: "any range on empty object", raw: "bytes=0-0", size: 0, invalid: true},
		{name: "suffix on empty object", raw: "bytes=-5", size: 0, invalid: true},
		{name: "start beyond int64", raw: "bytes=99999999999999999999-", size: 10, invalid: true},
		{name: "end beyond int64", raw: "bytes=2-99999999999999999999", size: 10, start: 2, end: 9, partial: true},
		{name: "suffix beyond int64", raw: "bytes=-99999999999999999999", size: 10, start: 0, end: 9, partial: true},
		{name: "reversed is ignored", raw: "bytes=4-2", size: 10},
		{name: "multi range is ignored", raw: "bytes=1-2,3-4", size: 10},
		{name: "multi range on empty object is ignored", raw: "bytes=0-1,5-6", size: 0},
		{name: "other unit is ignored", raw: "items=1-2", size: 10},
		{name: "no unit is ignored", raw: "1-2", size: 10},
		{name: "no dash is ignored", raw: "bytes=12", size: 10},
		{name: "bare dash is ignored", raw: "bytes=-", size: 10},
		{name: "letters are ignored", raw: "bytes=a-b", size: 10},
		{name: "signs are ignored", raw: "bytes=+1-2", size: 10},
		{name: "invalid on empty object is ignored", raw: "bytes=x-y", size: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseRange(test.raw, test.size)
			if test.invalid {
				var unsatisfiable *UnsatisfiableRangeError
				if !errors.Is(err, errs.ErrInvalidRange) || !errors.As(err, &unsatisfiable) {
					t.Fatalf("ParseRange(%q, %d) err = %v, want unsatisfiable", test.raw, test.size, err)
				}
				if unsatisfiable.Size != test.size || unsatisfiable.Requested != test.raw {
					t.Fatalf("error fields = %+v", unsatisfiable)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRange(%q, %d) err = %v", test.raw, test.size, err)
			}
			if got.Partial != test.partial || (test.partial && (got.Range.Start != test.start || got.Range.End != test.end)) {
				t.Fatalf("ParseRange(%q, %d) = %+v, want partial=%v %d-%d", test.raw, test.size, got, test.partial, test.start, test.end)
			}
		})
	}
}

func TestRangeMatrix(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		size         int64
		ignoreRange  bool
		status       int
		contentRange string
		length       int64
		unsatisfied  bool
	}{
		{name: "absent range", size: 100, status: http.StatusOK, length: 100},
		{name: "closed range", header: "bytes=10-19", size: 100, status: http.StatusPartialContent, contentRange: "bytes 10-19/100", length: 10},
		{name: "open ended range", header: "bytes=90-", size: 100, status: http.StatusPartialContent, contentRange: "bytes 90-99/100", length: 10},
		{name: "suffix range", header: "bytes=-5", size: 100, status: http.StatusPartialContent, contentRange: "bytes 95-99/100", length: 5},
		{name: "end past the end is clamped", header: "bytes=90-500", size: 100, status: http.StatusPartialContent, contentRange: "bytes 90-99/100", length: 10},
		{name: "suffix larger than object is whole object as 206", header: "bytes=-500", size: 100, status: http.StatusPartialContent, contentRange: "bytes 0-99/100", length: 100},
		{name: "start at size is unsatisfiable", header: "bytes=100-", size: 100, unsatisfied: true},
		{name: "start past size is unsatisfiable", header: "bytes=100-120", size: 100, unsatisfied: true},
		{name: "any range on zero byte object is unsatisfiable", header: "bytes=0-0", size: 0, unsatisfied: true},
		{name: "multi range gives whole object", header: "bytes=0-1,5-6", size: 100, status: http.StatusOK, length: 100},
		{name: "syntactically invalid gives whole object", header: "bytes=zz", size: 100, status: http.StatusOK, length: 100},
		{name: "other unit gives whole object", header: "pages=1-2", size: 100, status: http.StatusOK, length: 100},
		{name: "range ignored by if-range", header: "bytes=10-19", size: 100, ignoreRange: true, status: http.StatusOK, length: 100},
		{name: "unsatisfiable range ignored by if-range", header: "bytes=500-", size: 100, ignoreRange: true, status: http.StatusOK, length: 100},
		{name: "zero byte object without range", size: 0, status: http.StatusOK, length: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := NewPlan(test.header, test.size, test.ignoreRange)
			if test.unsatisfied {
				var unsatisfiable *UnsatisfiableRangeError
				if !errors.As(err, &unsatisfiable) || !errors.Is(err, errs.ErrInvalidRange) {
					t.Fatalf("NewPlan err = %v, want unsatisfiable", err)
				}
				if got, want := unsatisfiable.ContentRange(), "bytes */"+strconv.FormatInt(test.size, 10); got != want {
					t.Fatalf("ContentRange = %q, want %q", got, want)
				}
				header := http.Header{}
				SetUnsatisfiedHeaders(header, test.size)
				if header.Get("Content-Range") != unsatisfiable.ContentRange() || header.Get("Accept-Ranges") != "bytes" {
					t.Fatalf("unsatisfied headers = %v", header)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPlan err = %v", err)
			}
			if plan.Status != test.status || plan.ContentRange != test.contentRange || plan.Length != test.length {
				t.Fatalf("plan = %+v", plan)
			}
			header := http.Header{}
			plan.SetHeaders(header)
			if header.Get("Accept-Ranges") != "bytes" || header.Get("Content-Length") != strconv.FormatInt(test.length, 10) || header.Get("Content-Range") != test.contentRange {
				t.Fatalf("headers = %v", header)
			}
		})
	}
}

func TestPartNumberOnePlan(t *testing.T) {
	plan := PartNumberOnePlan(50)
	if plan.Status != http.StatusPartialContent || plan.ContentRange != "bytes 0-49/50" || plan.Length != 50 {
		t.Fatalf("plan = %+v", plan)
	}
	empty := PartNumberOnePlan(0)
	if empty.Status != http.StatusOK || empty.ContentRange != "" || empty.Length != 0 {
		t.Fatalf("empty plan = %+v", empty)
	}
}

type memorySource struct {
	data  []byte
	calls int
}

func (m *memorySource) ReadAt(_ context.Context, dst []byte, off int64) (int, error) {
	m.calls++
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(dst, m.data[off:])
	if off+int64(n) >= int64(len(m.data)) {
		return n, io.EOF
	}
	return n, nil
}

func testBytes(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	return data
}

func rangeServer(t *testing.T, source *memorySource) *httptest.Server {
	t.Helper()
	streamer := NewStreamer()
	size := int64(len(source.data))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan, err := NewPlan(r.Header.Get("Range"), size, false)
		if err != nil {
			SetUnsatisfiedHeaders(w.Header(), size)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		_ = streamer.Respond(r.Context(), w, plan, source, r.Method == http.MethodHead)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMediaServerRangeForms(t *testing.T) {
	body := testBytes(512)
	server := rangeServer(t, &memorySource{data: body})
	tests := []struct {
		name   string
		header string
		want   []byte
		crange string
		status int
	}{
		{name: "open ended", header: "bytes=100-", want: body[100:], crange: "bytes 100-511/512", status: http.StatusPartialContent},
		{name: "suffix", header: "bytes=-16", want: body[496:], crange: "bytes 496-511/512", status: http.StatusPartialContent},
		{name: "closed", header: "bytes=5-9", want: body[5:10], crange: "bytes 5-9/512", status: http.StatusPartialContent},
		{name: "full", want: body, status: http.StatusOK},
		{name: "multi range gives full", header: "bytes=0-1,5-6", want: body, status: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.header != "" {
				request.Header.Set("Range", test.header)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status || response.Header.Get("Content-Range") != test.crange {
				t.Fatalf("status/range = %d %q", response.StatusCode, response.Header.Get("Content-Range"))
			}
			if response.Header.Get("Accept-Ranges") != "bytes" || !bytes.Equal(got, test.want) {
				t.Fatalf("accept-ranges %q or body mismatch", response.Header.Get("Accept-Ranges"))
			}
		})
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Range", "bytes=999-1000")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */512" {
		t.Fatalf("invalid range status/range = %d %q", response.StatusCode, response.Header.Get("Content-Range"))
	}
}

func TestMediaServerHeadSendsNoBody(t *testing.T) {
	source := &memorySource{data: testBytes(64)}
	server := rangeServer(t, source)
	request, _ := http.NewRequest(http.MethodHead, server.URL, nil)
	request.Header.Set("Range", "bytes=0-9")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Length") != "10" || source.calls != 0 {
		t.Fatalf("status %d length %q calls %d", response.StatusCode, response.Header.Get("Content-Length"), source.calls)
	}
}

func TestStreamerChunksAcrossBufferBoundary(t *testing.T) {
	data := testBytes(StreamChunkSize*2 + 123)
	source := &memorySource{data: data}
	var out bytes.Buffer
	err := NewStreamer().Stream(context.Background(), &out, source, ByteRange{Start: 5, End: int64(len(data)) - 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data[5:]) || source.calls != 3 {
		t.Fatalf("body mismatch or calls = %d", source.calls)
	}
}

type failingSource struct{ err error }

func (f failingSource) ReadAt(context.Context, []byte, int64) (int, error) { return 0, f.err }

type stalledSource struct{}

func (stalledSource) ReadAt(context.Context, []byte, int64) (int, error) { return 0, nil }

func TestStreamerFailures(t *testing.T) {
	readFailure := errors.New("telegram unavailable")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		source RangeSource
		want   error
	}{
		{name: "source error", ctx: context.Background(), source: failingSource{err: readFailure}, want: readFailure},
		{name: "early eof", ctx: context.Background(), source: failingSource{err: io.EOF}, want: io.EOF},
		{name: "no progress", ctx: context.Background(), source: stalledSource{}, want: io.ErrNoProgress},
		{name: "canceled", ctx: canceled, source: &memorySource{data: testBytes(8)}, want: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NewStreamer().Stream(test.ctx, io.Discard, test.source, ByteRange{Start: 0, End: 7})
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestStreamerStopsOnWriteError(t *testing.T) {
	err := NewStreamer().Stream(context.Background(), failingWriter{}, &memorySource{data: testBytes(8)}, ByteRange{Start: 0, End: 7})
	if err == nil {
		t.Fatal("want write error")
	}
}
