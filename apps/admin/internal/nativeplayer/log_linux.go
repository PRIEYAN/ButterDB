//go:build linux

package nativeplayer

import (
	"fmt"
	"log/slog"
	"regexp"
)

var mediaURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:\d+/media/\S+`)

func linuxNativeLogf(format string, args ...any) {
	message := mediaURLPattern.ReplaceAllString(fmt.Sprintf(format, args...), "<media-url>")
	slog.Info("native player: " + message)
}
