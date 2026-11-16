package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/tlscert"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

const (
	adminTLSSelfSigned   = "self-signed"
	adminTLSFiles        = "files"
	adminTLSProxy        = "proxy"
	adminReadHeaderLimit = 10 * time.Second
	adminIdleLimit       = 120 * time.Second
	adminMaxHeaderBytes  = 64 << 10
)

var (
	_ tlsReporter  = (*adminTLS)(nil)
	_ http.Handler = clientAddressHandler{}
)

type adminTLS struct {
	mode string

	mu   sync.RWMutex
	cert *tls.Certificate
	spki [32]byte
	set  bool
}

func newAdminTLS(mode string) *adminTLS {
	return &adminTLS{mode: mode}
}

func (t *adminTLS) install(cert tls.Certificate, spki [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cert, t.spki, t.set = &cert, spki, true
}

func (t *adminTLS) info() adminv1.TLSInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.set {
		return adminv1.TLSInfo{Mode: t.mode}
	}
	return adminv1.TLSInfo{Mode: t.mode, SPKISHA256: tlscert.Fingerprint(t.spki)}
}

func (t *adminTLS) fingerprint() ([32]byte, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.spki, t.set
}

func (t *adminTLS) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.cert == nil {
		return nil, errors.New("core: admin: tls certificate is not loaded")
	}
	return t.cert, nil
}

func (t *adminTLS) config() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: t.getCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}

type adminCertificateSource struct {
	cfg      config.AdminConfig
	serverID string
	dataDir  string
	files    tlscert.Filesystem
	clock    clock.Clock
	random   io.Reader
	hostname func() (string, error)
}

func (s adminCertificateSource) load() (tls.Certificate, [32]byte, error) {
	if s.cfg.TLSMode == adminTLSFiles {
		return loadCertificateFiles(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	}
	material, err := tlscert.LoadOrCreateWith(tlscert.Config{
		Dir:       s.dataDir,
		ServerID:  s.serverID,
		Hostnames: s.cfg.Hostnames,
		Now:       s.clock.Now(),
		Random:    s.random,
		Hostname:  s.hostname,
		Files:     s.files,
	})
	if err != nil {
		return tls.Certificate{}, [32]byte{}, fmt.Errorf("core: admin: self-signed certificate: %w", err)
	}
	return material.Certificate, material.SPKISHA256, nil
}

func loadCertificateFiles(certFile, keyFile string) (tls.Certificate, [32]byte, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, [32]byte{}, fmt.Errorf("core: admin: load tls files: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, [32]byte{}, fmt.Errorf("core: admin: parse tls certificate: %w", err)
	}
	pair.Leaf = leaf
	return pair, sha256.Sum256(leaf.RawSubjectPublicKeyInfo), nil
}

type clientAddressHandler struct {
	trusted []netip.Prefix
	next    http.Handler
}

func (h clientAddressHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := session.ContextWithClientAddr(r.Context(), session.ResolveClientAddr(r, h.trusted))
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

func parseTrustedProxies(raw []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("core: admin: trusted proxy %q: %w", entry, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func adminAllowedHosts(cfg config.AdminConfig) []string {
	if cfg.TLSMode != adminTLSProxy && !listensOnLoopback(cfg.Listen) {
		return nil
	}
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	return append(hosts, cfg.Hostnames...)
}

type adminServer struct {
	cfg     config.AdminConfig
	handler http.Handler
	tls     *adminTLS
	logger  *slog.Logger

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

func newAdminServer(cfg config.AdminConfig, handler http.Handler, tlsState *adminTLS, logger *slog.Logger) *adminServer {
	return &adminServer{cfg: cfg, handler: handler, tls: tlsState, logger: logger}
}

func (s *adminServer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("core: admin: listen on %s: %w", s.cfg.Listen, err)
	}
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: adminReadHeaderLimit,
		IdleTimeout:       adminIdleLimit,
		MaxHeaderBytes:    adminMaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}
	if s.cfg.TLSMode != adminTLSProxy {
		server.TLSConfig = s.tls.config()
	}
	done := make(chan struct{})
	s.server, s.listener, s.done = server, listener, done
	go s.serve(server, listener, done)
	s.announce(listener.Addr().String())
	return nil
}

func (s *adminServer) announce(addr string) {
	attrs := []any{"component", "admin", "addr", addr, "tls_mode", s.cfg.TLSMode}
	if spki, known := s.tls.fingerprint(); known {
		attrs = append(attrs, "tls_fingerprint", tlscert.Fingerprint(spki), "tls_fingerprint_display", tlscert.DisplayFingerprint(spki))
	}
	s.logger.Info("admin listener", attrs...)
}

func (s *adminServer) serve(server *http.Server, listener net.Listener, done chan<- struct{}) {
	defer close(done)
	var err error
	if s.cfg.TLSMode == adminTLSProxy {
		err = server.Serve(listener)
	} else {
		err = server.ServeTLS(listener, "", "")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Error("admin listener stopped", "component", "admin", "error", err)
	}
}

func (s *adminServer) Close(ctx context.Context) error {
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
		return fmt.Errorf("core: admin: shutdown: %w", err)
	}
	<-done
	s.logger.Info("admin listener stopped", "component", "admin")
	return nil
}

func (s *adminServer) addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
