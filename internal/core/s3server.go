package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

const (
	s3ReadHeaderTimeout = 10 * time.Second
	s3IdleTimeout       = 120 * time.Second
	s3MaxHeaderBytes    = 64 << 10
	s3TLSModeFiles      = "files"
)

type s3Server struct {
	cfg     config.S3Config
	handler http.Handler
	logger  *slog.Logger

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

func newS3Server(cfg config.S3Config, handler http.Handler, logger *slog.Logger) *s3Server {
	return &s3Server{cfg: cfg, handler: handler, logger: logger}
}

func (s *s3Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("core: s3: listen on %s: %w", s.cfg.Listen, err)
	}
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s3ReadHeaderTimeout,
		IdleTimeout:       s3IdleTimeout,
		MaxHeaderBytes:    s3MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}
	done := make(chan struct{})
	s.server, s.listener, s.done = server, listener, done
	go s.serve(server, listener, done)
	s.logger.Info("s3 listener started", "component", "s3", "addr", listener.Addr().String())
	return nil
}

func (s *s3Server) serve(server *http.Server, listener net.Listener, done chan<- struct{}) {
	defer close(done)
	var err error
	if s.cfg.TLSMode == s3TLSModeFiles {
		err = server.ServeTLS(listener, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	} else {
		err = server.Serve(listener)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Error("s3 listener stopped", "component", "s3", "error", err)
	}
}

func (s *s3Server) Close(ctx context.Context) error {
	s.mu.Lock()
	server, done := s.server, s.done
	s.server, s.listener, s.done = nil, nil, nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		<-done
		return fmt.Errorf("core: s3: shutdown: %w", err)
	}
	<-done
	s.logger.Info("s3 listener stopped", "component", "s3")
	return nil
}

func (s *s3Server) addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func listensOnLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
