package model

import (
	"mime"
	"path"
	"strings"
)

type MediaKind string

const (
	MediaNone  MediaKind = ""
	MediaVideo MediaKind = "video"
	MediaAudio MediaKind = "audio"
)

var mediaExtensions = map[string]MediaKind{
	".mp4": MediaVideo, ".m4v": MediaVideo, ".mov": MediaVideo, ".qt": MediaVideo, ".webm": MediaVideo,
	".mkv": MediaVideo, ".mk3d": MediaVideo, ".avi": MediaVideo, ".ts": MediaVideo, ".m2ts": MediaVideo,
	".mts": MediaVideo, ".flv": MediaVideo, ".wmv": MediaVideo, ".ogv": MediaVideo, ".mpeg": MediaVideo,
	".mpg": MediaVideo,
	".mp3": MediaAudio, ".m4a": MediaAudio, ".aac": MediaAudio, ".wav": MediaAudio, ".flac": MediaAudio,
	".oga": MediaAudio, ".ogg": MediaAudio, ".opus": MediaAudio,
}

func MediaKindOf(key, contentType string) MediaKind {
	if kind, ok := mediaExtensions[strings.ToLower(path.Ext(key))]; ok {
		return kind
	}
	mediaType, _, _ := mime.ParseMediaType(strings.ToLower(contentType))
	switch {
	case strings.HasPrefix(mediaType, "video/"):
		return MediaVideo
	case strings.HasPrefix(mediaType, "audio/"), mediaType == "application/ogg":
		return MediaAudio
	}
	return MediaNone
}
