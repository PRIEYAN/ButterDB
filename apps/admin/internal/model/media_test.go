package model

import "testing"

func TestMediaKindOf(t *testing.T) {
	cases := []struct {
		key, contentType string
		want             MediaKind
	}{
		{"movies/clip.MP4", "", MediaVideo},
		{"a.mkv", "application/octet-stream", MediaVideo},
		{"song.mp3", "", MediaAudio},
		{"noext", "audio/flac", MediaAudio},
		{"noext", "video/webm; codecs=vp9", MediaVideo},
		{"pic.png", "image/png", MediaNone},
		{"notes.pdf", "application/pdf", MediaNone},
		{"x", "", MediaNone},
	}
	for _, tc := range cases {
		if got := MediaKindOf(tc.key, tc.contentType); got != tc.want {
			t.Fatalf("MediaKindOf(%q, %q) = %q, want %q", tc.key, tc.contentType, got, tc.want)
		}
	}
}
