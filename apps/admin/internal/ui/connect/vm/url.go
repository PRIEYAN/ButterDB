package vm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

const defaultAdminPort = "9443"

var (
	ErrInvalidURL           = errors.New("server address is not valid")
	ErrPlainHTTPNotLoopback = errors.New("plain http is allowed only for localhost, 127.0.0.1 and ::1")
)

func NormaliseURL(raw string) (string, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return "", fmt.Errorf("connect/vm: %w", ErrInvalidURL)
	}
	parsed, err := url.Parse(withScheme(input))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("connect/vm: %w", ErrInvalidURL)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("connect/vm: %w", ErrInvalidURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if scheme == "http" && !isLoopbackHost(host) {
		return "", fmt.Errorf("connect/vm: %w", ErrPlainHTTPNotLoopback)
	}
	port := parsed.Port()
	if port == "" {
		port = defaultAdminPort
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

func IsPlainHTTP(normalised string) bool {
	return strings.HasPrefix(normalised, "http://")
}

func withScheme(input string) string {
	if strings.Contains(input, "://") {
		return input
	}
	if net.ParseIP(input) != nil && strings.Contains(input, ":") {
		return "https://[" + input + "]"
	}
	return "https://" + input
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func hostLabel(normalised string) string {
	parsed, err := url.Parse(normalised)
	if err != nil {
		return normalised
	}
	return parsed.Hostname()
}
