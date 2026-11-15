package xmlcodec

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type bucketResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Name    string   `xml:"Name"`
	Created string   `xml:"CreationDate"`
}

type namespacedResult struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ PolicyStatus"`
	Public  bool     `xml:"IsPublic"`
}

func TestTimeFormat(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"whole second", time.Date(2026, 11, 12, 9, 41, 0, 0, time.UTC), "2026-11-12T09:41:00.000Z"},
		{"milliseconds kept", time.Date(2026, 11, 12, 9, 41, 7, 123_000_000, time.UTC), "2026-11-12T09:41:07.123Z"},
		{"sub millisecond truncated", time.Date(2026, 1, 2, 3, 4, 5, 999_999, time.UTC), "2026-01-02T03:04:05.000Z"},
		{"other zone converted", time.Date(2026, 11, 12, 10, 41, 0, 0, time.FixedZone("x", 3600)), "2026-11-12T09:41:00.000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatTime(tc.at); got != tc.want {
				t.Fatalf("FormatTime = %q, want %q", got, tc.want)
			}
			if got := tc.at.UTC().Format(TimeFormat); got != tc.want {
				t.Fatalf("Format(TimeFormat) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriteResult(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		status int
		want   string
	}{
		{"namespace added", bucketResult{Name: "photos", Created: "2026-11-12T09:41:00.000Z"}, http.StatusOK,
			`<ListAllMyBucketsResult xmlns="` + Namespace + `"><Name>photos</Name><CreationDate>2026-11-12T09:41:00.000Z</CreationDate></ListAllMyBucketsResult>`},
		{"namespace kept once", namespacedResult{Public: true}, http.StatusOK,
			`<PolicyStatus xmlns="` + Namespace + `"><IsPublic>true</IsPublic></PolicyStatus>`},
		{"status passed through", bucketResult{}, http.StatusCreated,
			`<ListAllMyBucketsResult xmlns="` + Namespace + `"><Name></Name><CreationDate></CreationDate></ListAllMyBucketsResult>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := WriteResult(rec, tc.status, tc.value); err != nil {
				t.Fatalf("WriteResult: %v", err)
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/xml" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
			want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + tc.want
			if got := rec.Body.String(); got != want {
				t.Fatalf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestWriteResultMarshalFailureWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteResult(rec, http.StatusOK, make(chan int)); err == nil {
		t.Fatal("WriteResult accepted an unmarshalable value")
	}
	if rec.Body.Len() != 0 || rec.Flushed {
		t.Fatalf("wrote %q before failing", rec.Body.String())
	}
}
