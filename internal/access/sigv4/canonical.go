package sigv4

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const upperHex = "0123456789ABCDEF"

func uriEncode(text string, encodeSlash bool) string {
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if isUnreserved(c) || (c == '/' && !encodeSlash) {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(upperHex[c>>4])
		out.WriteByte(upperHex[c&0x0f])
	}
	return out.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func canonicalURI(u *url.URL) (string, error) {
	escaped := u.EscapedPath()
	if escaped == "" {
		return "/", nil
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", err
	}
	return uriEncode(decoded, false), nil
}

type queryParam struct {
	name  string
	value string
}

func decodeQuery(rawQuery string) ([]queryParam, error) {
	var params []queryParam
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		rawName, rawValue, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return nil, err
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, err
		}
		params = append(params, queryParam{name: name, value: value})
	}
	return params, nil
}

func canonicalQuery(params []queryParam, skipName string) string {
	encoded := make([]queryParam, 0, len(params))
	for _, param := range params {
		if skipName != "" && param.name == skipName {
			continue
		}
		encoded = append(encoded, queryParam{name: uriEncode(param.name, true), value: uriEncode(param.value, true)})
	}
	slices.SortFunc(encoded, func(a, b queryParam) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.value, b.value)
	})
	parts := make([]string, len(encoded))
	for i, param := range encoded {
		parts[i] = param.name + "=" + param.value
	}
	return strings.Join(parts, "&")
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func signedHeaderValue(r *http.Request, name string) (string, bool) {
	if name == "host" {
		host := requestHost(r)
		return collapseSpaces(host), host != ""
	}
	values := r.Header.Values(name)
	if len(values) == 0 {
		if name == "content-length" && r.ContentLength > 0 {
			return strconv.FormatInt(r.ContentLength, 10), true
		}
		return "", false
	}
	trimmed := make([]string, len(values))
	for i, value := range values {
		trimmed[i] = collapseSpaces(value)
	}
	return strings.Join(trimmed, ","), true
}

func canonicalHeaders(r *http.Request, signedHeaders []string) (string, bool) {
	var out strings.Builder
	for _, name := range signedHeaders {
		value, ok := signedHeaderValue(r, name)
		if !ok {
			return "", false
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(value)
		out.WriteByte('\n')
	}
	return out.String(), true
}

type canonicalInput struct {
	request       *http.Request
	params        []queryParam
	skipParam     string
	signedHeaders []string
	hashedPayload string
}

func buildCanonicalRequest(in canonicalInput) (string, bool, error) {
	uri, err := canonicalURI(in.request.URL)
	if err != nil {
		return "", false, err
	}
	headers, present := canonicalHeaders(in.request, in.signedHeaders)
	if !present {
		return "", false, nil
	}
	lines := []string{
		in.request.Method,
		uri,
		canonicalQuery(in.params, in.skipParam),
		headers,
		strings.Join(in.signedHeaders, ";"),
		in.hashedPayload,
	}
	return strings.Join(lines, "\n"), true, nil
}

func parseSignedHeaders(value string) ([]string, bool) {
	if value == "" {
		return nil, false
	}
	names := strings.Split(value, ";")
	for _, name := range names {
		if name == "" || name != strings.ToLower(name) {
			return nil, false
		}
	}
	if !slices.IsSorted(names) || !slices.Contains(names, "host") {
		return nil, false
	}
	return names, true
}
