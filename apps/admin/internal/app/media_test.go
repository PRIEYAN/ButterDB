package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/media"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/mediaqueue"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

func TestPlayOrderStartsAtThePickedFileAndWraps(t *testing.T) {
	items := []adminv1.ObjectSummary{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	cases := map[int][]string{0: {"a", "b", "c"}, 1: {"b", "c", "a"}, 2: {"c", "a", "b"}, 9: {"c", "a", "b"}, -3: {"a", "b", "c"}}
	for start, want := range cases {
		var got []string
		for _, item := range playOrder(items, start) {
			got = append(got, item.Key)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("playOrder(%d) = %v, want %v", start, got, want)
		}
	}
	if playOrder(nil, 0) != nil {
		t.Fatal("empty queue should play nothing")
	}
}

func TestOpenErrorTextExplainsTheProblem(t *testing.T) {
	cases := map[error]string{
		nativeplayer.ErrUnsupported:                                  playerMissingText,
		fmt.Errorf("native player: mpv executable not found"):        playerMissingText,
		fmt.Errorf("open: %w", media.ErrFileNotFound):                playerGoneFileText,
		errors.New("native player: mpv exited during startup: boom"): playerOpenText,
	}
	for err, want := range cases {
		if got := openErrorText(err); got != want {
			t.Fatalf("openErrorText(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestQueueErrorTextNamesTheSkippedFile(t *testing.T) {
	err := fmt.Errorf("%w: %s: %w", mediaqueue.ErrDownload, "two.mp3", errors.New("boom"))
	if got := queueErrorText(err); !strings.Contains(got, "two.mp3") || !strings.HasPrefix(got, "Could not download") {
		t.Fatalf("queueErrorText = %q", got)
	}
	if queueErrorText(mediaqueue.ErrPlayback) != playerFailedText {
		t.Fatal("playback failure text changed")
	}
}
