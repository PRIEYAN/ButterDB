package viewer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type mediaStream struct {
	URL    string
	server *http.Server
	once   sync.Once
	err    error
}

func newStream(ctx context.Context, source Source, bucket string, meta adminv1.ObjectMeta, random io.Reader) (*mediaStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err := io.ReadFull(random, token); err != nil {
		return nil, fmt.Errorf("viewer: create stream token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("viewer: start stream: %w", err)
	}
	path := "/" + hex.EncodeToString(token)
	host := listener.Addr().String()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Host != host || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		serveMedia(ctx, source, bucket, meta, w, r)
	})
	stream := &mediaStream{URL: "http://" + host + path, server: server}
	go func() { _ = server.Serve(listener) }()
	context.AfterFunc(ctx, func() { _ = stream.Close() })
	return stream, nil
}

func (s *mediaStream) Close() error {
	s.once.Do(func() { s.err = s.server.Close() })
	return s.err
}

func serveMedia(parent context.Context, source Source, bucket string, meta adminv1.ObjectMeta, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if meta.ETag != "" {
		w.Header().Set("ETag", meta.ETag)
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))
		return
	}
	start, length, partial, err := mediaRange(r.Header.Get("Range"), meta.SizeBytes)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", meta.SizeBytes))
		http.Error(w, "Range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	defer cancel()
	opts := adminv1.GetObjectOptions{IfMatch: meta.ETag}
	if partial {
		opts.RangeStart, opts.RangeEnd = start, start+length-1
	}
	object, err := source.GetObject(ctx, bucket, meta.Key, opts)
	if err != nil {
		status := http.StatusBadGateway
		var apiErr *adminv1.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status <= 599 {
			status = apiErr.Status
		}
		http.Error(w, "Cannot read the file", status)
		return
	}
	defer object.Body.Close()
	if partial && start > 0 && object.Status != http.StatusPartialContent {
		http.Error(w, "The source did not honor the byte range", http.StatusBadGateway)
		return
	}
	if object.ContentLength >= 0 && object.ContentLength < length {
		http.Error(w, "The file response was incomplete", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	status := http.StatusOK
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, meta.SizeBytes))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	_, _ = io.CopyN(w, object.Body, length)
}

func mediaRange(header string, size int64) (start, length int64, partial bool, err error) {
	invalid := errors.New("viewer: invalid byte range")
	if size < 0 {
		return 0, 0, false, invalid
	}
	if header == "" {
		return 0, size, false, nil
	}
	value, ok := strings.CutPrefix(header, "bytes=")
	if !ok || size == 0 || strings.Contains(value, ",") {
		return 0, 0, false, invalid
	}
	left, right, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, false, invalid
	}
	if left == "" {
		n, e := strconv.ParseInt(right, 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, false, invalid
		}
		n = min(n, size)
		return size - n, n, true, nil
	}
	start, err = strconv.ParseInt(left, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, invalid
	}
	end := size - 1
	if right != "" {
		n, e := strconv.ParseInt(right, 10, 64)
		if e != nil || n < start {
			return 0, 0, false, invalid
		}
		end = min(n, end)
	}
	return start, end - start + 1, true, nil
}
