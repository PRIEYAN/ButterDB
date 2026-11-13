package logging

import (
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

func isRedactedKey(key string) bool {
	switch strings.ToLower(key) {
	case "password", "secret", "token", "api_hash", "authorization", "signature", "session", "code", "key_material":
		return true
	}
	return false
}

func New(w io.Writer, cfg config.LogConfig) *slog.Logger {
	options := &slog.HandlerOptions{
		Level:       ParseLevel(cfg.Level),
		ReplaceAttr: ReplaceAttr,
	}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(w, options))
	}
	return slog.New(slog.NewJSONHandler(w, options))
}

func ParseLevel(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return formatTime(a)
	}
	if isRedactedKey(a.Key) {
		return slog.String(a.Key, redactedText)
	}
	return a
}

func formatTime(a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindTime {
		return a
	}
	return slog.String(a.Key, a.Value.Time().In(time.UTC).Format(timeLayout))
}
