package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type Session struct {
	mu        sync.Mutex
	token     string
	url       string
	file      LogicalFile
	mime      string
	source    Source
	ctx       context.Context
	cancel    context.CancelFunc
	lastTouch time.Time
	closed    bool

	readMu sync.Mutex
	body   io.ReadCloser
	pos    int64
}

func newSession(parent context.Context, file LogicalFile, source Source) (*Session, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &Session{
		token:     token,
		file:      file,
		mime:      mimeFor(file),
		source:    source,
		ctx:       ctx,
		cancel:    cancel,
		lastTouch: time.Now(),
	}, nil
}

func (s *Session) Token() string {
	if s == nil {
		return ""
	}
	return s.token
}

func (s *Session) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *Session) setURL(url string) {
	s.mu.Lock()
	s.url = url
	s.mu.Unlock()
}

func (s *Session) Size() int64 { return s.file.Size }

func (s *Session) Name() string { return s.file.Name }

func (s *Session) MimeType() string { return s.mime }

func (s *Session) Encrypted() bool { return s.file.Encrypted }

func (s *Session) File() LogicalFile { return s.file }

func (s *Session) touch() {
	s.mu.Lock()
	s.lastTouch = time.Now()
	s.mu.Unlock()
}

func (s *Session) LastTouch() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTouch
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.readMu.Lock()
	s.dropBody()
	s.readMu.Unlock()
}

func (s *Session) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= s.file.Size {
		return 0, io.EOF
	}
	s.touch()
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return 0, ErrSessionNotFound
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.body == nil || s.pos != off {
		if err := s.open(off); err != nil {
			return 0, err
		}
	}
	want := min(int64(len(p)), s.file.Size-off)
	n, err := io.ReadFull(s.body, p[:want])
	s.pos += int64(n)
	if err != nil {
		s.dropBody()
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			if n > 0 {
				return n, nil
			}
			return 0, io.ErrUnexpectedEOF
		}
		return n, err
	}
	if s.pos >= s.file.Size {
		s.dropBody()
	}
	return n, nil
}

func (s *Session) open(off int64) error {
	s.dropBody()
	stream, err := s.source.GetObject(s.ctx, s.file.Bucket, s.file.Key, adminv1.GetObjectOptions{
		IfMatch:    s.file.ETag,
		RangeStart: off,
		RangeEnd:   s.file.Size - 1,
	})
	if err != nil {
		var apiErr *adminv1.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusPreconditionFailed {
			return ErrStaleRevision
		}
		return fmt.Errorf("media: read %s: %w", s.file.Name, err)
	}
	if off > 0 && stream.Status != http.StatusPartialContent {
		_ = stream.Body.Close()
		return fmt.Errorf("media: source ignored the byte range")
	}
	s.body = stream.Body
	s.pos = off
	return nil
}

func (s *Session) dropBody() {
	if s.body != nil {
		_ = s.body.Close()
		s.body = nil
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func mimeFor(file LogicalFile) string {
	if typ := contentTypeFor(file.Name); typ != "application/octet-stream" || file.ContentType == "" {
		return typ
	}
	return file.ContentType
}
