package vm

import (
	"context"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

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

func TestFocusLoadsDetailsWithoutReadingObjectBytes(t *testing.T) {
	c := newFakeClient()
	c.metas["song.mp3"] = adminv1.ObjectMeta{Key: "song.mp3", ContentType: "audio/mpeg", SizeBytes: 10}
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "b", "")
	v.Focus(context.Background(), "song.mp3")
	st := v.State().Get()
	if st.Details.Meta == nil || st.Details.Meta.Key != "song.mp3" || st.Details.Loading {
		t.Fatalf("details = %+v", st.Details)
	}
	if len(c.getCalls) != 0 {
		t.Fatalf("focusing an object read its bytes: %v", c.getCalls)
	}
}
