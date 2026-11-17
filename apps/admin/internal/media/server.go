package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	neturl "net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mediaRoutePrefix       = "/media/file/"
	mediaSessionIdleTTL    = 2 * time.Hour
	mediaSessionSweepEvery = 5 * time.Minute
	mediaStreamChunkSize   = 256 * 1024
)

var mediaStreamBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, mediaStreamChunkSize)
		return &buf
	},
}

type Server struct {
	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	baseURL  string
	closed   bool
	sessions map[string]*Session
}

func NewServer() *Server {
	return &Server{
		sessions: make(map[string]*Session),
	}
}

func (s *Server) Add(session *Session) error {
	if session == nil {
		return fmt.Errorf("media: nil session")
	}
	if err := s.ensureStarted(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionNotFound
	}
	s.sessions[session.Token()] = session
	url := s.baseURL + mediaRoutePrefix + session.Token() + "/" + neturl.PathEscape(session.Name())
	s.mu.Unlock()
	session.setURL(url)
	return nil
}

func (s *Server) CloseSession(token string) error {
	s.mu.Lock()
	session, ok := s.sessions[token]
	if ok {
		delete(s.sessions, token)
	}
	s.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	session.Close()
	return nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	server := s.server
	listener := s.listener
	s.server = nil
	s.listener = nil
	s.baseURL = ""
	s.closed = true
	sessions := s.sessions
	s.sessions = make(map[string]*Session)
	s.mu.Unlock()

	for _, session := range sessions {
		session.Close()
	}
	if server != nil {
		_ = server.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
	return nil
}

func (s *Server) ensureStarted() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionNotFound
	}
	if s.server != nil {
		s.mu.Unlock()
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("media: listen loopback: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(mediaRoutePrefix, s.handleFile)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.listener = ln
	s.server = srv
	s.baseURL = "http://" + ln.Addr().String()
	s.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	go s.sweepIdleSessions(srv)
	return nil
}

func (s *Server) sweepIdleSessions(server *http.Server) {
	ticker := time.NewTicker(mediaSessionSweepEvery)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		if s.server != server || s.closed {
			s.mu.Unlock()
			return
		}
		now := time.Now()
		expired := make([]*Session, 0)
		for token, session := range s.sessions {
			if session == nil || now.Sub(session.LastTouch()) < mediaSessionIdleTTL {
				continue
			}
			delete(s.sessions, token)
			expired = append(expired, session)
		}
		s.mu.Unlock()

		for _, session := range expired {
			session.Close()
		}
	}
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	s.handleSessionBytes(w, r, mediaRoutePrefix, (*Session).ReadAt)
}

func (s *Server) handleSessionBytes(w http.ResponseWriter, r *http.Request, prefix string, readAt func(*Session, context.Context, []byte, int64) (int, error)) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	setMediaCORS(w.Header())
	token, _, _ := strings.Cut(strings.TrimPrefix(path.Clean(r.URL.Path), prefix), "/")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	session := s.session(token)
	if session == nil {
		http.NotFound(w, r)
		return
	}

	size := session.Size()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", session.MimeType())
	setMediaNoStore(w.Header())
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	start, end, partial, ok := parseRangeHeader(r.Header.Get("Range"), size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if !partial {
		start, end = 0, size-1
	}

	length := int64(0)
	if size > 0 {
		length = end - start + 1
	}
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead || length == 0 {
		return
	}
	if err := streamSessionRange(r.Context(), w, session, start, length, readAt); err != nil {
		return
	}
}

func setMediaNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func setMediaCORS(header http.Header) {
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Range")
	header.Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range")
	header.Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) session(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[token]
}

func parseRangeHeader(raw string, size int64) (start, end int64, partial bool, ok bool) {
	if raw == "" {
		return 0, 0, false, true
	}
	if size <= 0 || !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return 0, 0, true, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(raw, "bytes="))
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, true, false
	}
	switch {
	case a == "":
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, true, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, true
	case b == "":
		first, err := strconv.ParseInt(a, 10, 64)
		if err != nil || first < 0 || first >= size {
			return 0, 0, true, false
		}
		return first, size - 1, true, true
	default:
		first, err1 := strconv.ParseInt(a, 10, 64)
		last, err2 := strconv.ParseInt(b, 10, 64)
		if err1 != nil || err2 != nil || first < 0 || last < first || first >= size {
			return 0, 0, true, false
		}
		if last >= size {
			last = size - 1
		}
		return first, last, true, true
	}
}

func streamSessionRange(ctx context.Context, w io.Writer, session *Session, start, length int64, readAt func(*Session, context.Context, []byte, int64) (int, error)) error {
	bufPtr := mediaStreamBufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer mediaStreamBufferPool.Put(bufPtr)
	var sent int64
	for sent < length {
		want := length - sent
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, err := readAt(session, ctx, buf[:want], start+sent)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			sent += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && sent == length {
				return nil
			}
			return err
		}
	}
	return nil
}

type streamTypeInfo struct {
	kind        StreamKind
	mime        string
	imageFormat string
}

var streamTypesByExt = map[string]streamTypeInfo{
	".mp4":      {kind: StreamKindVideo, mime: "video/mp4"},
	".m4v":      {kind: StreamKindVideo, mime: "video/mp4"},
	".mov":      {kind: StreamKindVideo, mime: "video/quicktime"},
	".qt":       {kind: StreamKindVideo, mime: "video/quicktime"},
	".webm":     {kind: StreamKindVideo, mime: "video/webm"},
	".mkv":      {kind: StreamKindVideo, mime: "video/x-matroska"},
	".mk3d":     {kind: StreamKindVideo, mime: "video/x-matroska"},
	".avi":      {kind: StreamKindVideo, mime: "video/x-msvideo"},
	".ts":       {kind: StreamKindVideo, mime: "video/mp2t"},
	".m2ts":     {kind: StreamKindVideo, mime: "video/mp2t"},
	".mts":      {kind: StreamKindVideo, mime: "video/mp2t"},
	".flv":      {kind: StreamKindVideo, mime: "video/x-flv"},
	".wmv":      {kind: StreamKindVideo, mime: "video/x-ms-wmv"},
	".ogv":      {kind: StreamKindVideo, mime: "video/ogg"},
	".mpeg":     {kind: StreamKindVideo, mime: "video/mpeg"},
	".mpg":      {kind: StreamKindVideo, mime: "video/mpeg"},
	".mp3":      {kind: StreamKindAudio, mime: "audio/mpeg"},
	".m4a":      {kind: StreamKindAudio, mime: "audio/mp4"},
	".aac":      {kind: StreamKindAudio, mime: "audio/aac"},
	".wav":      {kind: StreamKindAudio, mime: "audio/wav"},
	".flac":     {kind: StreamKindAudio, mime: "audio/flac"},
	".oga":      {kind: StreamKindAudio, mime: "audio/ogg"},
	".ogg":      {kind: StreamKindAudio, mime: "audio/ogg"},
	".opus":     {kind: StreamKindAudio, mime: "audio/ogg"},
	".pdf":      {kind: StreamKindPDF, mime: "application/pdf"},
	".jpg":      {kind: StreamKindImage, mime: "image/jpeg", imageFormat: "jpeg"},
	".jpeg":     {kind: StreamKindImage, mime: "image/jpeg", imageFormat: "jpeg"},
	".png":      {kind: StreamKindImage, mime: "image/png", imageFormat: "png"},
	".gif":      {kind: StreamKindImage, mime: "image/gif", imageFormat: "gif"},
	".webp":     {kind: StreamKindImage, mime: "image/webp", imageFormat: "webp"},
	".bmp":      {kind: StreamKindImage, mime: "image/bmp", imageFormat: "bmp"},
	".txt":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".log":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".md":       {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".markdown": {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".csv":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".tsv":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".json":     {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".yaml":     {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".yml":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".toml":     {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".xml":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".srt":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
	".vtt":      {kind: StreamKindText, mime: "text/plain; charset=utf-8"},
}

func streamTypeForName(name string) (streamTypeInfo, bool) {
	ext := path.Ext(name)
	if ext == "" {
		return streamTypeInfo{}, false
	}
	info, ok := streamTypesByExt[strings.ToLower(ext)]
	return info, ok
}

func contentTypeFor(name string) string {
	if info, ok := streamTypeForName(name); ok {
		return info.mime
	}
	if typ := mime.TypeByExtension(path.Ext(name)); typ != "" {
		return typ
	}
	return "application/octet-stream"
}

func streamKindForName(name string) StreamKind {
	if info, ok := streamTypeForName(name); ok {
		return info.kind
	}
	return StreamKindUnknown
}
