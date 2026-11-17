package media

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type Service struct {
	ctx    context.Context
	source Source
	server *Server
}

func NewService(ctx context.Context, source Source) *Service {
	return &Service{ctx: ctx, source: source, server: NewServer()}
}

func (s *Service) Open(ctx context.Context, bucket, key string) (OpenResult, error) {
	if s == nil || s.source == nil {
		return OpenResult{}, fmt.Errorf("media: source not ready")
	}
	meta, err := s.source.ObjectMeta(ctx, bucket, key)
	if err != nil {
		var apiErr *adminv1.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return OpenResult{}, ErrFileNotFound
		}
		return OpenResult{}, err
	}
	if kind := KindFor(key, meta.ContentType); kind != StreamKindVideo && kind != StreamKindAudio {
		return OpenResult{}, ErrUnsupportedMediaType
	}
	file := LogicalFile{
		Bucket:      bucket,
		Key:         key,
		Name:        path.Base(key),
		Size:        meta.SizeBytes,
		ContentType: meta.ContentType,
		ETag:        meta.ETag,
		Encrypted:   meta.Encrypted,
		Protected:   meta.Encrypted,
	}
	session, err := newSession(s.ctx, file, s.source)
	if err != nil {
		return OpenResult{}, err
	}
	if err := s.server.Add(session); err != nil {
		session.Close()
		return OpenResult{}, err
	}
	return s.OpenResultForToken(session.Token())
}

func (s *Service) OpenResultForToken(token string) (OpenResult, error) {
	if s == nil || s.server == nil || token == "" {
		return OpenResult{}, ErrSessionNotFound
	}
	session := s.server.session(token)
	if session == nil {
		return OpenResult{}, ErrSessionNotFound
	}
	file := session.File()
	return OpenResult{
		Token:         session.Token(),
		URL:           session.URL(),
		Name:          file.Name,
		Kind:          KindFor(file.Name, file.ContentType),
		MimeType:      session.MimeType(),
		SupportsRange: true,
		Info:          file,
	}, nil
}

func (s *Service) CloseSession(token string) error {
	if s == nil || s.server == nil {
		return ErrSessionNotFound
	}
	return s.server.CloseSession(token)
}

func (s *Service) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}

func KindFor(name, contentType string) StreamKind {
	if kind := streamKindForName(name); kind != StreamKindUnknown {
		return kind
	}
	mediaType, _, _ := mime.ParseMediaType(strings.ToLower(contentType))
	switch {
	case strings.HasPrefix(mediaType, "video/"):
		return StreamKindVideo
	case strings.HasPrefix(mediaType, "audio/"), mediaType == "application/ogg":
		return StreamKindAudio
	}
	return StreamKindUnknown
}
