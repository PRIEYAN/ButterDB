package vm

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func imageStream(data []byte, length int64) func() adminv1.ObjectStream {
	return func() adminv1.ObjectStream {
		return adminv1.ObjectStream{Body: io.NopCloser(bytes.NewReader(data)), Status: 200, ContentLength: length}
	}
}

func TestPreviewSizeCeiling(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		metaSize    int64
		streamLen   int64
		body        []byte
		wantStatus  PreviewStatus
		wantFetch   bool
	}{
		{"within ceiling", "image/png", 1024, 4, []byte("data"), PreviewReady, true},
		{"exactly at ceiling by header", "image/png", PreviewMaxBytes, 4, []byte("data"), PreviewReady, true},
		{"meta above ceiling never fetched", "image/png", PreviewMaxBytes + 1, 4, []byte("data"), PreviewTooLarge, false},
		{"content length above ceiling", "image/jpeg", 1024, PreviewMaxBytes + 1, []byte("data"), PreviewTooLarge, true},
		{"body streams past ceiling", "image/gif", 1024, -1, bytes.Repeat([]byte{1}, PreviewMaxBytes+10), PreviewTooLarge, true},
		{"unsupported type never fetched", "application/pdf", 10, 4, []byte("x"), PreviewUnsupported, false},
		{"charset parameter accepted", "image/webp; charset=binary", 10, 4, []byte("data"), PreviewReady, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient()
			c.metas["img"] = adminv1.ObjectMeta{Key: "img", ContentType: tc.contentType, SizeBytes: tc.metaSize}
			c.streams["img"] = imageStream(tc.body, tc.streamLen)
			v, _ := newTestVM(t, c)
			v.Open(context.Background(), "photos", "")
			v.Focus(context.Background(), "img")
			st := v.State().Get()
			if st.Preview.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", st.Preview.Status, tc.wantStatus)
			}
			if fetched := len(c.getCalls) > 0; fetched != tc.wantFetch {
				t.Fatalf("fetched = %v, want %v", fetched, tc.wantFetch)
			}
			if tc.wantStatus == PreviewReady && string(st.Preview.Data) != string(tc.body) {
				t.Fatal("preview bytes differ")
			}
			if st.Details.Meta == nil || st.Details.Meta.Key != "img" {
				t.Fatalf("details = %+v", st.Details)
			}
		})
	}
}

func TestPreviewUnsupportedMessage(t *testing.T) {
	c := newFakeClient()
	c.metas["doc"] = adminv1.ObjectMeta{Key: "doc", ContentType: "application/pdf", SizeBytes: 5}
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "b", "")
	v.Focus(context.Background(), "doc")
	if got := v.State().Get().Preview.Message; got != "No preview for application/pdf" {
		t.Fatalf("message = %q", got)
	}
}

func TestFocusMetaError(t *testing.T) {
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "b", "")
	v.Focus(context.Background(), "gone")
	st := v.State().Get()
	if st.Details.Problem == nil || st.Details.Problem.Code != adminv1.CodeObjectNotFound || st.Details.Meta != nil {
		t.Fatalf("details = %+v", st.Details)
	}
}

func TestExceedsPixelCeiling(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{{4000, 3000, false}, {8000, 5000, false}, {8001, 5000, true}, {0, 10, true}, {-1, 10, true}}
	for _, tc := range cases {
		if got := ExceedsPixelCeiling(tc.w, tc.h); got != tc.want {
			t.Errorf("ExceedsPixelCeiling(%d,%d) = %v", tc.w, tc.h, got)
		}
	}
}
