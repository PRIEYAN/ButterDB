package objects

import (
	"context"
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type playCall struct {
	bucket string
	keys   []string
	start  int
}

type recordingPlayer struct{ calls []playCall }

func (p *recordingPlayer) Play(_ context.Context, bucket string, items []adminv1.ObjectSummary, start int, _ func(string)) {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	p.calls = append(p.calls, playCall{bucket, keys, start})
}

func mediaFolder() *fakeClient {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{
		{Key: "a.mp3", ContentType: "audio/mpeg"},
		{Key: "notes.pdf", ContentType: "application/pdf"},
		{Key: "b.mp4", ContentType: "video/mp4"},
		{Key: "c.flac"},
	}}
	return client
}

func TestQueuePlayQueuesAllFolderMediaFromTheTopOrCheckedFiles(t *testing.T) {
	h := newHarness(t, mediaFolder())
	player := &recordingPlayer{}
	h.browser.opts.Player = player
	h.vmodel.Open(context.Background(), "music", "")

	h.ui(func() { h.browser.playMedia() })
	h.ui(func() { h.browser.onRowSelected(rowCell(2)) })
	h.ui(func() { h.browser.playMedia() })
	h.vmodel.ToggleObject("c.flac")
	h.vmodel.ToggleObject("a.mp3")
	h.ui(func() { h.browser.playMedia() })

	want := []string{"music:a.mp3,b.mp4,c.flac@0", "music:a.mp3,b.mp4,c.flac@0", "music:a.mp3,c.flac@0"}
	if len(player.calls) != len(want) {
		t.Fatalf("calls = %+v", player.calls)
	}
	for i, c := range player.calls {
		if got := c.bucket + ":" + strings.Join(c.keys, ",") + "@" + string(rune('0'+c.start)); got != want[i] {
			t.Fatalf("call %d = %s, want %s", i, got, want[i])
		}
	}
}

func TestActivatingMediaRowPlaysAndOtherRowsShowDetails(t *testing.T) {
	h := newHarness(t, mediaFolder())
	player := &recordingPlayer{}
	h.browser.opts.Player = player
	h.vmodel.Open(context.Background(), "music", "")

	h.ui(func() { h.browser.activateRow(2) })
	if len(player.calls) != 1 || player.calls[0].start != 1 || len(player.calls[0].keys) != 3 {
		t.Fatalf("double-click on b.mp4 = %+v", player.calls)
	}
	h.ui(func() { h.browser.activateRow(1) })
	if len(player.calls) != 1 {
		t.Fatal("a PDF must not open the player")
	}
}

func TestPlayWithNothingPlayableInforms(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "notes.pdf", ContentType: "application/pdf"}}}
	h := newHarness(t, client)
	player := &recordingPlayer{}
	h.browser.opts.Player = player
	h.vmodel.Open(context.Background(), "docs", "")
	h.ui(func() { h.browser.playMedia() })
	if len(player.calls) != 0 || h.win.Canvas().Overlays().Top() == nil {
		t.Fatal("expected a notice and no playback")
	}
}

func rowCell(row int) widget.TableCellID {
	return widget.TableCellID{Row: row, Col: colName}
}

func (h *harness) ui(fn func()) {
	h.browser.uiMu.Lock()
	defer h.browser.uiMu.Unlock()
	fn()
}
