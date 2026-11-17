package embedded

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

var errNoEngine = errors.New("butterDB engine is not running")

func serveInProcess(handler http.Handler, req *http.Request) (*http.Response, error) {
	if handler == nil {
		return nil, errNoEngine
	}
	inner := req.Clone(req.Context())
	inner.RemoteAddr = "127.0.0.1:1"
	inner.RequestURI = req.URL.RequestURI()
	if inner.Host == "" {
		inner.Host = req.URL.Host
	}
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	reader, writer := io.Pipe()
	w := &pipeWriter{header: make(http.Header), body: writer, ready: make(chan struct{})}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = writer.CloseWithError(errors.New("embedded: handler panicked"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = writer.Close()
		}()
		handler.ServeHTTP(w, inner)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		_ = reader.CloseWithError(req.Context().Err())
		return nil, req.Context().Err()
	}
	resp := &http.Response{
		Status:        http.StatusText(w.status),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.sent,
		Body:          reader,
		ContentLength: -1,
		Request:       req,
	}
	if req.Method == http.MethodHead {
		_ = reader.Close()
		resp.Body = http.NoBody
	}
	return resp, nil
}

type pipeWriter struct {
	header http.Header
	sent   http.Header
	body   *io.PipeWriter
	status int
	once   sync.Once
	ready  chan struct{}
}

func (w *pipeWriter) Header() http.Header {
	return w.header
}

func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		w.sent = w.header.Clone()
		close(w.ready)
	})
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

func (w *pipeWriter) Flush() {
	w.WriteHeader(http.StatusOK)
}
