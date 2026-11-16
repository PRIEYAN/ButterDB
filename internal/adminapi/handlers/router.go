package handlers

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	requestIDBytes   = 16
	jsonRouteTimeout = 60 * time.Second
	relayRouteBudget = 75 * time.Second
)

type api struct {
	deps           Dependencies
	mux            *http.ServeMux
	trustedProxies []netip.Prefix
	allowedHosts   []string
	idMu           sync.Mutex
}

func NewRouter(deps Dependencies) (http.Handler, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	proxies, err := parsePrefixes(deps.Config.TrustedProxies)
	if err != nil {
		return nil, err
	}
	a := &api{deps: deps, mux: http.NewServeMux(), trustedProxies: proxies, allowedHosts: lowerAll(deps.Config.AllowedHosts)}
	a.registerRoutes()
	return a, nil
}

func validateDependencies(deps Dependencies) error {
	missing := ""
	switch {
	case deps.Buckets == nil:
		missing = "buckets"
	case deps.Keys == nil:
		missing = "keys"
	case deps.Lister == nil:
		missing = "lister"
	case deps.Writer == nil:
		missing = "writer"
	case deps.Server == nil:
		missing = "server"
	case deps.Presign == nil:
		missing = "presign"
	case deps.Status == nil:
		missing = "status"
	case deps.Audit == nil:
		missing = "audit"
	case deps.Setup == nil:
		missing = "setup"
	case deps.Pairing == nil:
		missing = "pairing"
	case deps.Devices == nil:
		missing = "devices"
	case deps.Device == nil:
		missing = "device resolver"
	case deps.Auth == nil:
		missing = "auth"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return fmt.Errorf("handlers: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}

func parsePrefixes(raw []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("handlers: trusted proxy %q: %w", entry, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func lowerAll(values []string) []string {
	lowered := make([]string, 0, len(values))
	for _, value := range values {
		lowered = append(lowered, strings.ToLower(value))
	}
	return lowered
}

func (a *api) public(pattern string, h routeHandler) {
	a.mux.Handle(pattern, a.jsonDeadlines(jsonRouteTimeout, a.route(h)))
}

func (a *api) publicRelay(pattern string, h routeHandler) {
	a.mux.Handle(pattern, a.jsonDeadlines(relayRouteBudget, a.route(h)))
}

func (a *api) authed(pattern string, h routeHandler) {
	a.mux.Handle(pattern, a.jsonDeadlines(jsonRouteTimeout, a.deps.Auth(a.route(h))))
}

func (a *api) authedRelay(pattern string, h routeHandler) {
	a.mux.Handle(pattern, a.jsonDeadlines(relayRouteBudget, a.deps.Auth(a.route(h))))
}

func (a *api) authedStream(pattern string, h routeHandler) {
	a.mux.Handle(pattern, a.deps.Auth(a.route(h)))
}

func (a *api) jsonDeadlines(budget time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		deadline := a.deps.Clock.Now().Add(budget)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		next.ServeHTTP(w, r)
	})
}

func (a *api) registerRoutes() {
	a.registerSetupRoutes()
	a.registerTelegramRoutes()
	a.registerReplicaRoutes()
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := a.newRequestID()
	w.Header().Set(adminv1.HeaderRequestID, id)
	r = r.WithContext(withRequestID(r.Context(), id))
	defer a.recoverPanic(w, r)
	if !a.hostAllowed(r.Host) {
		a.writeError(w, r, failure(adminv1.CodeMisdirectedRequest, nil))
		return
	}
	if _, pattern := a.mux.Handler(r); pattern == "" {
		a.routeMiss(w, r)
		return
	}
	a.mux.ServeHTTP(w, r)
}

func (a *api) recoverPanic(w http.ResponseWriter, r *http.Request) {
	if recovered := recover(); recovered != nil {
		a.writeError(w, r, fmt.Errorf("handlers: panic: %v", recovered))
	}
}

type missProbe struct {
	header http.Header
	status int
}

func (p *missProbe) Header() http.Header         { return p.header }
func (p *missProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *missProbe) WriteHeader(status int)      { p.status = status }

func (a *api) routeMiss(w http.ResponseWriter, r *http.Request) {
	probe := &missProbe{header: http.Header{}}
	a.mux.ServeHTTP(probe, r)
	if probe.status == http.StatusMethodNotAllowed {
		if allow := probe.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		a.writeError(w, r, failure(adminv1.CodeMethodNotAllowed, nil))
		return
	}
	a.writeError(w, r, failure(adminv1.CodeNotFound, nil))
}

func (a *api) newRequestID() string {
	buf := make([]byte, requestIDBytes)
	a.idMu.Lock()
	_, err := a.deps.Random.Read(buf)
	a.idMu.Unlock()
	if err != nil {
		return fmt.Sprintf("%032x", a.deps.Clock.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (a *api) hostAllowed(hostport string) bool {
	if len(a.allowedHosts) == 0 {
		return true
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, allowed := range a.allowedHosts {
		if subtle.ConstantTimeCompare([]byte(host), []byte(allowed)) == 1 {
			return true
		}
	}
	return false
}

func (a *api) clientAddress(r *http.Request) string {
	peer := peerAddress(r.RemoteAddr)
	if !peer.IsValid() || !a.isTrustedProxy(peer) {
		return peer.String()
	}
	forwarded := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		candidate, err := netip.ParseAddr(strings.TrimSpace(forwarded[i]))
		if err != nil {
			return peer.String()
		}
		if !a.isTrustedProxy(candidate) {
			return candidate.String()
		}
	}
	return peer.String()
}

func peerAddress(remote string) netip.Addr {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func (a *api) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range a.trustedProxies {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func parseBoolQuery(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, &BadRequestError{Field: "query"}
	}
	return parsed, nil
}
