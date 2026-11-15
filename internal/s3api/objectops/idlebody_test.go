package objectops

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

type timeoutReadError struct{}

func (timeoutReadError) Error() string   { return "injected stalled body" }
func (timeoutReadError) Timeout() bool   { return true }
func (timeoutReadError) Temporary() bool { return true }

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, timeoutReadError{} }
func (timeoutBody) Close() error             { return nil }

type advancingBody struct {
	clock  *clock.Fake
	chunks []string
	index  int
}

func (b *advancingBody) Read(buffer []byte) (int, error) {
	if b.index >= len(b.chunks) {
		return 0, io.EOF
	}
	chunk := b.chunks[b.index]
	b.index++
	n := copy(buffer, chunk)
	b.clock.Advance(20 * time.Second)
	return n, nil
}

func (*advancingBody) Close() error { return nil }

func putWithBody(f *fixture, t *testing.T, body io.ReadCloser, length int64, recorder http.ResponseWriter) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/photos/stalled.bin", body)
	r.ContentLength = length
	f.handlers.PutObject(recorder, r, routeFor(router.OpPutObject, "stalled.bin"), fullAuth(length))
}

func TestStalledPutBodyTimesOut(t *testing.T) {
	f := newFixture(t, fixtureOptions{idle: 10 * time.Second})
	recorder := newDeadlineRecorder()
	putWithBody(f, t, timeoutBody{}, 5, recorder)
	doc := expectError(t, recorder, 400, "RequestTimeout")
	if doc.Message == "" {
		t.Fatal("timeout carried no message")
	}
	deadlines := recorder.deadlines
	if len(deadlines) != 2 || !deadlines[0].Equal(f.clock.Now().Add(10*time.Second)) || !deadlines[1].IsZero() {
		t.Fatalf("deadlines = %v", deadlines)
	}
	if next := f.put(t, "next.bin", "ok", nil); next.Code != http.StatusOK {
		t.Fatalf("a stalled body blocked the next write: status %d", next.Code)
	}
}

func TestActiveLongPutUsesIdleNotAbsoluteDeadline(t *testing.T) {
	f := newFixture(t, fixtureOptions{idle: 10 * time.Second})
	started := f.clock.Now()
	body := &advancingBody{clock: f.clock, chunks: []string{"one", "two", "three"}}
	recorder := newDeadlineRecorder()
	putWithBody(f, t, body, 11, recorder)
	if recorder.Code != http.StatusOK || string(f.writer.last(t).body) != "onetwothree" {
		t.Fatalf("status = %d body = %q", recorder.Code, f.writer.last(t).body)
	}
	if elapsed := f.clock.Now().Sub(started); elapsed <= 10*time.Second {
		t.Fatalf("the test did not exceed one idle interval: %s", elapsed)
	}
	deadlines := recorder.deadlines
	if len(deadlines) < 8 || len(deadlines)%2 != 0 {
		t.Fatalf("deadline calls = %v, want a set and a clear around every read", deadlines)
	}
	for index := 0; index < len(deadlines); index += 2 {
		if deadlines[index].IsZero() || !deadlines[index+1].IsZero() {
			t.Fatalf("deadline pair %d = %v/%v", index/2, deadlines[index], deadlines[index+1])
		}
		if index >= 2 && !deadlines[index].After(deadlines[index-2]) {
			t.Fatalf("the idle deadline was not renewed: %v", deadlines)
		}
	}
}

type plainRecorder struct {
	*httptest.ResponseRecorder
}

func TestBodyDeadlineSetupFailsClosed(t *testing.T) {
	f := newFixture(t, fixtureOptions{idle: 10 * time.Second})
	recorder := plainRecorder{httptest.NewRecorder()}
	recorder.Header().Set("X-Amz-Request-Id", testRequestID)
	putWithBody(f, t, io.NopCloser(strings.NewReader("data")), 4, recorder)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want fail-closed 500", recorder.Code)
	}
}

func TestBodyDeadlineDisabledWhenNegative(t *testing.T) {
	f := newFixture(t, fixtureOptions{idle: -1})
	recorder := plainRecorder{httptest.NewRecorder()}
	putWithBody(f, t, io.NopCloser(strings.NewReader("data")), 4, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestBodyReadTimeoutClassification(t *testing.T) {
	if !isBodyReadTimeout(timeoutReadError{}) {
		t.Fatal("a timeout net.Error was not classified")
	}
	if isBodyReadTimeout(errors.New("ordinary")) {
		t.Fatal("an ordinary error was classified as a timeout")
	}
	if isBodyReadTimeout(nil) {
		t.Fatal("nil was classified as a timeout")
	}
}
