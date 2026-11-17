package objects

import (
	"context"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type openedFile struct {
	bucket string
	object adminv1.ObjectSummary
}

type recordingOpener struct{ files []openedFile }

func (o *recordingOpener) Open(_ context.Context, bucket string, object adminv1.ObjectSummary) {
	o.files = append(o.files, openedFile{bucket, object})
}

func TestDoubleClickObjectAsksBeforeOpening(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{
		{Key: "movie.mp4", ContentType: "video/mp4", SizeBytes: 1024},
		{Key: "song.mp3", ContentType: "audio/mpeg"},
	}}
	h := newHarness(t, client)
	opener := &recordingOpener{}
	h.browser.opts.Opener = opener
	h.vmodel.Open(context.Background(), "photos", "")
	cell := h.browser.createCell().(*cell)
	cell.row = 0
	cell.DoubleTapped(nil)
	popup := h.win.Canvas().Overlays().Top()
	if popup == nil {
		t.Fatal("double-click did not show the Open popup")
	}
	if len(opener.files) != 0 {
		t.Fatal("file opened before pressing Open")
	}
	h.vmodel.Open(context.Background(), "another-bucket", "")
	test.Tap(findButton(popup, "Open"))
	if len(opener.files) != 1 || opener.files[0].bucket != "photos" || opener.files[0].object.Key != "movie.mp4" {
		t.Fatalf("opened files = %+v, want the object originally activated", opener.files)
	}
}

func TestCancelObjectPopupDoesNotOpenFile(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "cat.png", ContentType: "image/png"}}}
	h := newHarness(t, client)
	opener := &recordingOpener{}
	h.browser.opts.Opener = opener
	h.vmodel.Open(context.Background(), "photos", "")
	h.browser.activateRow(0)
	popup := h.win.Canvas().Overlays().Top()
	if popup == nil {
		t.Fatal("missing Open popup")
	}
	test.Tap(findButton(popup, "Cancel"))
	if len(opener.files) != 0 || h.win.Canvas().Overlays().Top() != nil {
		t.Fatal("Cancel should dismiss the popup without opening a file")
	}
}
