package xmlcodec

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type LongResponse struct {
	w       http.ResponseWriter
	control *http.ResponseController
	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	closed  bool
	failed  error
}

func StartLongResponse(w http.ResponseWriter, interval time.Duration) (*LongResponse, error) {
	if interval <= 0 {
		return nil, errors.New("xmlcodec: keepalive interval must be positive")
	}
	header := w.Header()
	header.Set("Content-Type", "application/xml")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Del("Content-Length")
	l := &LongResponse{
		w:       w,
		control: http.NewResponseController(w),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xmlDeclaration)); err != nil {
		return nil, fmt.Errorf("xmlcodec: start long response: %w", err)
	}
	if err := l.control.Flush(); err != nil {
		return nil, fmt.Errorf("xmlcodec: start long response: %w", err)
	}
	go l.keepAlive(interval)
	return l, nil
}

func (l *LongResponse) keepAlive(interval time.Duration) {
	defer close(l.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.writeSpace()
		}
	}
}

func (l *LongResponse) writeSpace() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.failed != nil {
		return
	}
	l.failed = l.writeAndFlush([]byte(" "))
}

func (l *LongResponse) writeAndFlush(b []byte) error {
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	return l.control.Flush()
}

func (l *LongResponse) Close() error {
	l.mu.Lock()
	already := l.closed
	l.closed = true
	l.mu.Unlock()
	if !already {
		close(l.stop)
	}
	<-l.done
	return nil
}

func (l *LongResponse) Succeed(v any) error {
	document, err := marshalDocument(v)
	if err != nil {
		_ = l.Close()
		return err
	}
	return l.finish(document)
}

func (l *LongResponse) Fail(errorXML []byte) error {
	return l.finish(errorXML)
}

func (l *LongResponse) finish(document []byte) error {
	if err := l.Close(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return fmt.Errorf("xmlcodec: long response: %w", l.failed)
	}
	if err := l.writeAndFlush(document); err != nil {
		return fmt.Errorf("xmlcodec: long response: %w", err)
	}
	return nil
}
