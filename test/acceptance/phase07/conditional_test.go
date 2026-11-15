package phase07

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
)

func serveObject(t *testing.T, read *core.Read, streamer *httprange.Streamer, now time.Time, header http.Header, bucketName, key string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	meta, opened, err := read.Open(context.Background(), bucketName, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer opened.Close()
	entity := httprange.EntityFromMeta(meta)
	result, err := httprange.Evaluate(httprange.ConditionsFromHeader(header), entity, now)
	var precondition *httprange.PreconditionError
	switch {
	case errors.As(err, &precondition):
		if !errors.Is(err, errs.ErrPreconditionFailed) {
			t.Fatalf("precondition error does not wrap ErrPreconditionFailed: %v", err)
		}
		recorder.Header().Set("X-Failed-Condition", precondition.Condition)
		recorder.WriteHeader(http.StatusPreconditionFailed)
		return recorder
	case err != nil:
		t.Fatalf("evaluate: %v", err)
	case result.NotModified:
		httprange.WriteNotModified(recorder, entity, "", "")
		return recorder
	}
	plan, err := httprange.NewPlan(header.Get("Range"), meta.PlaintextSize, result.IgnoreRange)
	var unsatisfiable *httprange.UnsatisfiableRangeError
	if errors.As(err, &unsatisfiable) {
		httprange.SetUnsatisfiedHeaders(recorder.Header(), meta.PlaintextSize)
		recorder.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return recorder
	}
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	httprange.SetValidators(recorder.Header(), entity)
	if err := streamer.Respond(context.Background(), recorder, plan, opened, false); err != nil {
		t.Fatalf("respond: %v", err)
	}
	return recorder
}

func TestConditionalGETAndInvalidRangeFollowHTTPRules(t *testing.T) {
	for _, encryption := range []string{"none", "tde1"} {
		t.Run("bucket encryption "+encryption, func(t *testing.T) {
			w := newWorld(t)
			w.createBucket("docs", encryption)
			plain := randomBytes("conditional body", 1000)
			meta := w.mustPut("docs", "page.bin", plain)
			read := w.newRead(t.TempDir(), noReadAhead())
			t.Cleanup(func() { _ = read.Close(context.Background()) })
			streamer := httprange.NewStreamer()

			lastModified := time.Unix(meta.MsgDate, 0).UTC()
			now := lastModified.Add(time.Hour)
			etag := `"` + meta.ETag + `"`
			size := strconv.Itoa(len(plain))
			dateHeader := func(at time.Time) string { return at.UTC().Format(http.TimeFormat) }

			tests := []struct {
				name        string
				header      http.Header
				status      int
				contentType string
				wantRange   string
				body        []byte
				noTelegram  bool
			}{
				{name: "no conditions returns the whole object", header: http.Header{}, status: 200, body: plain},
				{name: "If-None-Match with the current ETag is 304", header: http.Header{"If-None-Match": {etag}}, status: 304, noTelegram: true},
				{name: "If-None-Match star is 304", header: http.Header{"If-None-Match": {"*"}}, status: 304, noTelegram: true},
				{name: "If-None-Match with another ETag is 200", header: http.Header{"If-None-Match": {`"other"`}}, status: 200, body: plain},
				{name: "If-Modified-Since at the last modified time is 304", header: http.Header{"If-Modified-Since": {dateHeader(lastModified)}}, status: 304, noTelegram: true},
				{name: "If-Modified-Since before the last modified time is 200", header: http.Header{"If-Modified-Since": {dateHeader(lastModified.Add(-time.Hour))}}, status: 200, body: plain},
				{name: "If-None-Match false with If-Modified-Since true is 200", header: http.Header{"If-None-Match": {`"other"`}, "If-Modified-Since": {dateHeader(lastModified)}}, status: 200, body: plain},
				{name: "If-Match with another ETag is 412", header: http.Header{"If-Match": {`"other"`}}, status: 412, noTelegram: true},
				{name: "If-Unmodified-Since before the last modified time is 412", header: http.Header{"If-Unmodified-Since": {dateHeader(lastModified.Add(-time.Hour))}}, status: 412, noTelegram: true},
				{name: "If-Match true with If-Unmodified-Since false is 200", header: http.Header{"If-Match": {etag}, "If-Unmodified-Since": {dateHeader(lastModified.Add(-time.Hour))}}, status: 200, body: plain},
				{name: "If-Match star matches the existing object", header: http.Header{"If-Match": {"*"}}, status: 200, body: plain},
				{name: "closed range is 206 with Content-Range", header: http.Header{"Range": {"bytes=10-19"}}, status: 206, wantRange: "bytes 10-19/" + size, body: plain[10:20]},
				{name: "open ended range is 206", header: http.Header{"Range": {"bytes=990-"}}, status: 206, wantRange: "bytes 990-999/" + size, body: plain[990:]},
				{name: "suffix range is 206", header: http.Header{"Range": {"bytes=-5"}}, status: 206, wantRange: "bytes 995-999/" + size, body: plain[995:]},
				{name: "end past the size is clamped", header: http.Header{"Range": {"bytes=900-5000"}}, status: 206, wantRange: "bytes 900-999/" + size, body: plain[900:]},
				{name: "suffix larger than the object returns all of it as 206", header: http.Header{"Range": {"bytes=-5000"}}, status: 206, wantRange: "bytes 0-999/" + size, body: plain},
				{name: "start at the size is 416 with bytes star size", header: http.Header{"Range": {"bytes=1000-"}}, status: 416, wantRange: "bytes */" + size, noTelegram: true},
				{name: "start beyond the size is 416", header: http.Header{"Range": {"bytes=5000-6000"}}, status: 416, wantRange: "bytes */" + size, noTelegram: true},
				{name: "multiple ranges are ignored and the whole object is 200", header: http.Header{"Range": {"bytes=0-1,5-6"}}, status: 200, body: plain},
				{name: "a syntactically invalid range is ignored", header: http.Header{"Range": {"bytes=abc"}}, status: 200, body: plain},
				{name: "a unit other than bytes is ignored", header: http.Header{"Range": {"items=0-5"}}, status: 200, body: plain},
				{name: "If-Range with a matching ETag honours the range", header: http.Header{"Range": {"bytes=0-9"}, "If-Range": {etag}}, status: 206, wantRange: "bytes 0-9/" + size, body: plain[:10]},
				{name: "If-Range with another ETag ignores the range", header: http.Header{"Range": {"bytes=0-9"}, "If-Range": {`"other"`}}, status: 200, body: plain},
				{name: "a failed condition wins over an invalid range", header: http.Header{"Range": {"bytes=5000-"}, "If-Match": {`"other"`}}, status: 412, noTelegram: true},
				{name: "a satisfied If-None-Match wins over a range", header: http.Header{"Range": {"bytes=0-9"}, "If-None-Match": {etag}}, status: 304, noTelegram: true},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					readsBefore := w.client.ReadCount()
					got := serveObject(t, read, streamer, now, tt.header, "docs", "page.bin")
					if got.Code != tt.status {
						t.Fatalf("status = %d, want %d", got.Code, tt.status)
					}
					if tt.wantRange != "" && got.Header().Get("Content-Range") != tt.wantRange {
						t.Fatalf("Content-Range = %q, want %q", got.Header().Get("Content-Range"), tt.wantRange)
					}
					if tt.wantRange == "" && got.Header().Get("Content-Range") != "" {
						t.Fatalf("Content-Range = %q, want none", got.Header().Get("Content-Range"))
					}
					if !bytes.Equal(got.Body.Bytes(), tt.body) {
						t.Fatalf("body = %d bytes, want %d", got.Body.Len(), len(tt.body))
					}
					if tt.noTelegram && w.client.ReadCount() != readsBefore {
						t.Fatalf("status %d read %d bytes ranges from Telegram, want none", tt.status, w.client.ReadCount()-readsBefore)
					}
					assertStatusHeaders(t, got, tt.status, meta.ETag, len(tt.body))
				})
			}
		})
	}
}

func assertStatusHeaders(t *testing.T, got *httptest.ResponseRecorder, status int, etag string, bodyLen int) {
	t.Helper()
	switch status {
	case 200, 206:
		if got.Header().Get("Accept-Ranges") != "bytes" || got.Header().Get("Content-Length") != strconv.Itoa(bodyLen) {
			t.Fatalf("headers = %v, want Accept-Ranges bytes and Content-Length %d", got.Header(), bodyLen)
		}
		if got.Header().Get("ETag") == "" || got.Header().Get("Last-Modified") == "" {
			t.Fatalf("headers = %v, want ETag and Last-Modified", got.Header())
		}
	case 304:
		if got.Header().Get("ETag") == "" || got.Header().Get("Last-Modified") == "" || got.Body.Len() != 0 {
			t.Fatalf("304 headers = %v body %d, want validators and no body", got.Header(), got.Body.Len())
		}
	case 416:
		if got.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf("416 headers = %v, want Accept-Ranges bytes", got.Header())
		}
	}
	if etag == "" {
		t.Fatal("stored ETag is empty")
	}
}
