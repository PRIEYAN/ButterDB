package router

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	headerOrigin         = "Origin"
	headerRequestMethod  = "Access-Control-Request-Method"
	headerRequestHeaders = "Access-Control-Request-Headers"
	preflightVary        = "Origin, Access-Control-Request-Headers, Access-Control-Request-Method"
	corsWildcard         = "*"
	preflightMissingText = "Insufficient information. Origin request header needed."
)

type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

func DefaultExposeHeaders() []string {
	return []string{
		"ETag", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified",
		"Content-Type", "Content-Disposition", "Content-Encoding", "Cache-Control", "Expires",
		"x-amz-request-id", "x-amz-id-2", "x-amz-server-side-encryption",
		"x-amz-checksum-crc32", "x-amz-checksum-crc32c", "x-amz-checksum-crc64nvme",
		"x-amz-checksum-sha1", "x-amz-checksum-sha256",
	}
}

func (c CORSConfig) Enabled() bool {
	return len(c.AllowedOrigins) > 0
}

func (c CORSConfig) ExposedHeaders() []string {
	if len(c.ExposeHeaders) == 0 {
		return DefaultExposeHeaders()
	}
	return c.ExposeHeaders
}

func (c CORSConfig) originAllowed(origin string) bool {
	for _, allowed := range c.AllowedOrigins {
		if allowed == corsWildcard || originMatches(allowed, origin) {
			return true
		}
	}
	return false
}

func (c CORSConfig) allowOriginValue(origin string) string {
	for _, allowed := range c.AllowedOrigins {
		if allowed == corsWildcard {
			return corsWildcard
		}
	}
	return origin
}

func originMatches(pattern, origin string) bool {
	pattern = strings.ToLower(pattern)
	origin = strings.ToLower(origin)
	before, after, wildcard := strings.Cut(pattern, corsWildcard)
	if !wildcard {
		return pattern == origin
	}
	return len(origin) >= len(before)+len(after) && strings.HasPrefix(origin, before) && strings.HasSuffix(origin, after)
}

func (c CORSConfig) methodAllowed(method string) bool {
	for _, allowed := range c.AllowedMethods {
		if strings.EqualFold(allowed, method) {
			return true
		}
	}
	return false
}

func (c CORSConfig) headersAllowed(requested string) bool {
	for _, header := range strings.Split(requested, ",") {
		header = strings.TrimSpace(header)
		if header != "" && !c.headerAllowed(header) {
			return false
		}
	}
	return true
}

func (c CORSConfig) headerAllowed(header string) bool {
	for _, allowed := range c.AllowedHeaders {
		if allowed == corsWildcard || strings.EqualFold(allowed, header) {
			return true
		}
	}
	return false
}

func (c CORSConfig) applyActual(h http.Header, r *http.Request) {
	origin := r.Header.Get(headerOrigin)
	if !c.Enabled() || origin == "" || !c.originAllowed(origin) {
		return
	}
	h.Set("Access-Control-Allow-Origin", c.allowOriginValue(origin))
	h.Set("Access-Control-Expose-Headers", strings.Join(c.ExposedHeaders(), ", "))
	h.Add("Vary", "Origin")
}

func (c CORSConfig) preflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get(headerOrigin)
	method := r.Header.Get(headerRequestMethod)
	if origin == "" || method == "" {
		s3err.Write(w, r, s3err.New(s3err.CodeBadRequest, preflightMissingText))
		return
	}
	requested := r.Header.Get(headerRequestHeaders)
	if !c.Enabled() || !c.originAllowed(origin) || !c.methodAllowed(method) || !c.headersAllowed(requested) {
		s3err.Write(w, r, s3err.New(s3err.CodeAccessForbidden, ""))
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", c.allowOriginValue(origin))
	h.Set("Access-Control-Allow-Methods", strings.Join(c.AllowedMethods, ", "))
	if requested != "" {
		h.Set("Access-Control-Allow-Headers", requested)
	}
	h.Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAgeSeconds))
	h.Set("Access-Control-Expose-Headers", strings.Join(c.ExposedHeaders(), ", "))
	h.Add("Vary", preflightVary)
	w.WriteHeader(http.StatusOK)
}
