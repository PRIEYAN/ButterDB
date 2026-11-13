package main

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

const (
	maxSocketPathBytes = 103
	minRequestBudget   = 5 * time.Second
)

const (
	regionPattern     = `^[a-z0-9-]{1,32}$`
	dnsLabelPattern   = `^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`
	pathPrefixPattern = `^(/[a-z0-9-]+)+$`
)

func matches(pattern, text string) bool {
	return regexp.MustCompile(pattern).MatchString(text)
}

type listener struct {
	key  string
	host string
	port string
}

func configRules(cfg config.Config) []config.Finding {
	var found []config.Finding
	found = append(found, listenerRules(cfg)...)
	found = append(found, s3Rules(cfg.S3)...)
	found = append(found, adminRules(cfg.Admin)...)
	found = append(found, mediaRules(cfg.Media)...)
	found = append(found, opsRules(cfg.Ops)...)
	if cfg.Telegram.TestDC {
		found = append(found, warningFinding("telegram.test_dc", "enabled; Telegram test data centres are for development only"))
	}
	return found
}

func splitListen(key, address string) (listener, bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return listener{}, false
	}
	return listener{key: key, host: host, port: port}, true
}

func isWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenersOverlap(a, b listener) bool {
	if a.port != b.port || a.port == "0" {
		return false
	}
	return isWildcardHost(a.host) || isWildcardHost(b.host) || a.host == b.host
}

func listenerRules(cfg config.Config) []config.Finding {
	candidates := []struct{ key, address string }{
		{"s3.listen", cfg.S3.Listen},
		{"admin.listen", cfg.Admin.Listen},
		{"ops.listen", cfg.Ops.Listen},
	}
	if cfg.Media.Enabled {
		candidates = append(candidates, struct{ key, address string }{"media.listen", cfg.Media.Listen})
	}
	var parsed []listener
	var found []config.Finding
	for _, c := range candidates {
		current, ok := splitListen(c.key, c.address)
		if !ok {
			continue
		}
		for _, earlier := range parsed {
			if listenersOverlap(earlier, current) {
				found = append(found, errorFinding(current.key, fmt.Sprintf("port %s overlaps %s", current.port, earlier.key)))
			}
		}
		parsed = append(parsed, current)
	}
	return found
}

func s3Rules(cfg config.S3Config) []config.Finding {
	var found []config.Finding
	if cfg.PublicURL == "" {
		found = append(found, warningFinding("s3.public_url", "empty; presigned URLs are unavailable"))
	} else if !validPublicURL(cfg.PublicURL) {
		found = append(found, errorFinding("s3.public_url", "must be an absolute http or https URL without query or fragment"))
	}
	for _, domain := range cfg.BaseDomains {
		if !validDNSName(domain) {
			found = append(found, errorFinding("s3.base_domains", fmt.Sprintf("%q is not a valid DNS name", domain)))
		}
	}
	if !matches(regionPattern, cfg.Region) {
		found = append(found, errorFinding("s3.region", "must match ^[a-z0-9-]{1,32}$"))
	}
	if cfg.RequestBudget.Std() < minRequestBudget {
		found = append(found, errorFinding("s3.request_budget", "must be at least 5s"))
	}
	for _, origin := range cfg.CORSAllowedOrigins {
		if !validCORSOrigin(origin) {
			found = append(found, errorFinding("s3.cors_allowed_origins", fmt.Sprintf("%q is neither * nor scheme://host[:port]", origin)))
		}
	}
	if cfg.TLSMode == "files" {
		found = append(found, requirePair("s3.tls_cert_file", cfg.TLSCertFile, "s3.tls_key_file", cfg.TLSKeyFile)...)
	}
	return found
}

func requirePair(certKey, cert, keyKey, key string) []config.Finding {
	var found []config.Finding
	if cert == "" {
		found = append(found, errorFinding(certKey, "required when tls_mode is files"))
	}
	if key == "" {
		found = append(found, errorFinding(keyKey, "required when tls_mode is files"))
	}
	return found
}

func validPublicURL(raw string) bool {
	if strings.ContainsAny(raw, "?#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !matches(dnsLabelPattern, label) {
			return false
		}
	}
	return true
}

func validCORSOrigin(origin string) bool {
	if origin == "*" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	if u.Scheme == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, "?#") {
		return false
	}
	return true
}

func adminRules(cfg config.AdminConfig) []config.Finding {
	var found []config.Finding
	switch cfg.TLSMode {
	case "files":
		found = append(found, requirePair("admin.tls_cert_file", cfg.TLSCertFile, "admin.tls_key_file", cfg.TLSKeyFile)...)
	case "proxy":
		if current, ok := splitListen("admin.listen", cfg.Listen); ok && !isLoopbackHost(current.host) {
			found = append(found, errorFinding("admin.listen", "must be a loopback address when tls_mode is proxy"))
		}
	}
	if len(cfg.ControlSocket) > maxSocketPathBytes {
		found = append(found, errorFinding("admin.control_socket", fmt.Sprintf("path is %d bytes; the limit is %d", len(cfg.ControlSocket), maxSocketPathBytes)))
	}
	return found
}

func mediaRules(cfg config.MediaConfig) []config.Finding {
	if !cfg.Enabled {
		return nil
	}
	var found []config.Finding
	if cfg.PathPrefix != "" && !matches(pathPrefixPattern, cfg.PathPrefix) {
		found = append(found, errorFinding("media.path_prefix", "must match ^(/[a-z0-9-]+)+$"))
	}
	if current, ok := splitListen("media.listen", cfg.Listen); ok && isLoopbackHost(current.host) && len(cfg.Hostnames) == 0 && publicHost(cfg.PublicURL) == "" {
		found = append(found, errorFinding("media.listen", "loopback listener needs media.hostnames or a media.public_url host"))
	}
	found = append(found, widthLadderRules(cfg)...)
	if len(cfg.WorkerSocket) > maxSocketPathBytes {
		found = append(found, errorFinding("media.worker_socket", fmt.Sprintf("path is %d bytes; the limit is %d", len(cfg.WorkerSocket), maxSocketPathBytes)))
	}
	if cfg.WorkerMode == "spawn" && cfg.WorkerSandbox == "off" {
		found = append(found, warningFinding("media.worker_sandbox", "off; no sandbox around the decoders"))
	}
	return found
}

func publicHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func widthLadderRules(cfg config.MediaConfig) []config.Finding {
	ladder := cfg.WidthLadder
	if len(ladder) == 0 {
		return []config.Finding{errorFinding("media.width_ladder", "must not be empty")}
	}
	for i, width := range ladder {
		if width < 1 || (i > 0 && width <= ladder[i-1]) {
			return []config.Finding{errorFinding("media.width_ladder", "must be strictly ascending and at least 1")}
		}
	}
	if ladder[len(ladder)-1] != cfg.MaxOutputSide {
		return []config.Finding{errorFinding("media.width_ladder", "last value must equal media.max_output_side")}
	}
	return nil
}

func opsRules(cfg config.OpsConfig) []config.Finding {
	current, ok := splitListen("ops.listen", cfg.Listen)
	if !ok || isLoopbackHost(current.host) {
		return nil
	}
	if cfg.MetricsTokenFile == "" && !cfg.AllowUnauthenticatedMetrics {
		return []config.Finding{errorFinding("ops.listen", "not loopback; set ops.metrics_token_file or ops.allow_unauthenticated_metrics")}
	}
	return nil
}
