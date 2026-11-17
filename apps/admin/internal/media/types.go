package media

import (
	"context"
	"errors"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

var (
	ErrFileNotFound         = errors.New("media: file not found")
	ErrUnsupportedMediaType = errors.New("media: unsupported media type")
	ErrStaleRevision        = errors.New("media: stale file revision")
	ErrSessionNotFound      = errors.New("media: session not found")
)

type Source interface {
	ObjectMeta(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error)
	GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error)
}

type StreamKind string

const (
	StreamKindUnknown StreamKind = "unknown"
	StreamKindVideo   StreamKind = "video"
	StreamKindAudio   StreamKind = "audio"
	StreamKindPDF     StreamKind = "pdf"
	StreamKindText    StreamKind = "text"
	StreamKindImage   StreamKind = "image"
)

type LogicalFile struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	ETag        string `json:"etag"`
	Encrypted   bool   `json:"encrypted"`
	Protected   bool   `json:"protected"`
}

type OpenResult struct {
	Token         string      `json:"token"`
	URL           string      `json:"url"`
	Name          string      `json:"name"`
	Kind          StreamKind  `json:"kind"`
	MimeType      string      `json:"mime_type"`
	SupportsRange bool        `json:"supports_range"`
	Info          LogicalFile `json:"info"`
}
