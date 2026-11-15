package router

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const hostNotServedMessage = "Host header is not served by this endpoint"

func ParseTarget(r *http.Request, a Addressing) (Target, error) {
	host := hostWithoutPort(r.Host)
	domains := normalisedDomains(a.BaseDomains)
	if a.LoopbackOnly && !hostServed(host, domains, a.AllowedHosts) {
		return Target{}, s3err.New(s3err.CodeInvalidRequest, hostNotServedMessage)
	}
	rawPath := r.URL.EscapedPath()
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		return Target{}, invalidURI()
	}
	if name, ok := virtualHostedBucket(host, domains); ok {
		return virtualHostedTarget(name, rawPath)
	}
	return pathStyleTarget(rawPath)
}

func virtualHostedTarget(name, rawPath string) (Target, error) {
	key, err := decodeSegment(strings.TrimPrefix(rawPath, "/"))
	if err != nil {
		return Target{}, err
	}
	target := Target{Bucket: name, Key: key, HasKey: key != "", VirtualHosted: true}
	return target, checkBucketName(target.Bucket)
}

func pathStyleTarget(rawPath string) (Target, error) {
	trimmed := strings.TrimPrefix(rawPath, "/")
	if trimmed == "" {
		return Target{}, nil
	}
	rawBucket, rawKey, _ := strings.Cut(trimmed, "/")
	name, err := decodeSegment(rawBucket)
	if err != nil {
		return Target{}, err
	}
	key, err := decodeSegment(rawKey)
	if err != nil {
		return Target{}, err
	}
	target := Target{Bucket: name, Key: key, HasKey: key != ""}
	return target, checkBucketName(target.Bucket)
}

func decodeSegment(raw string) (string, error) {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", invalidURI()
	}
	return decoded, nil
}

func invalidURI() error {
	return s3err.New(s3err.CodeInvalidURI, "")
}

func checkBucketName(name string) error {
	if err := bucket.BucketName(name).Validate(); err != nil {
		return fmt.Errorf("router: bucket name: %w", err)
	}
	return nil
}

func hostWithoutPort(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(host)
}

func normalisedDomains(domains []string) []string {
	cleaned := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
		if domain != "" {
			cleaned = append(cleaned, domain)
		}
	}
	sort.SliceStable(cleaned, func(i, j int) bool { return len(cleaned[i]) > len(cleaned[j]) })
	return cleaned
}

func virtualHostedBucket(host string, domains []string) (string, bool) {
	for _, domain := range domains {
		suffix := "." + domain
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return strings.TrimSuffix(host, suffix), true
		}
	}
	return "", false
}

func hostServed(host string, domains, allowedHosts []string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	for _, allowed := range allowedHosts {
		if hostWithoutPort(allowed) == host {
			return true
		}
	}
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
