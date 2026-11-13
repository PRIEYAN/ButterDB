package wire

import (
	"encoding/base64"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	pairSeparator    = "|"
	userMetaPrefix   = "m."
	systemMetaPrefix = "s."
	wrappedKeyBytes  = 72
	keyCheckBytes    = 61
	maxPartCount     = 10000
)

func ExtractHeaderLine(raw string) string {
	head, _, _ := strings.Cut(raw, "\n")
	return head
}

func HasPrefix(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(ExtractHeaderLine(raw)), Prefix)
}

func splitHeader(raw string) (map[string]string, error) {
	header := strings.TrimSpace(ExtractHeaderLine(raw))
	if header == "" {
		return nil, ErrWireMissingHeader
	}
	if len(header) > MaxTextChars {
		return nil, ErrWireTooLong
	}
	parts := strings.Split(header, pairSeparator)
	if len(parts) < 2 {
		return nil, ErrWireMalformed
	}
	if parts[0] != Prefix {
		return nil, ErrWireBadVersion
	}
	pairs := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			return nil, ErrWireMalformed
		}
		pairs[part[:eq]] = part[eq+1:]
	}
	return pairs, nil
}

func allBytes(s string, accept func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !accept(s[i]) {
			return false
		}
	}
	return true
}

func isLowerHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

func isDigitByte(c byte) bool {
	return c >= '0' && c <= '9'
}

func isLowerHex(s string, length int) bool {
	return len(s) == length && allBytes(s, isLowerHexByte)
}

func isJobID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		case 19:
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return false
			}
		default:
			if !isLowerHexByte(c) {
				return false
			}
		}
	}
	return true
}

func isKey(s string) bool {
	return len(s) >= 1 && len(s) <= MaxKeyBytes && utf8.ValidString(s) && strings.IndexByte(s, 0) < 0
}

func isContentType(s string) bool {
	return len(s) >= 1 && len(s) <= MaxContentTypeBytes && allBytes(s, func(c byte) bool { return c >= 0x20 && c <= 0x7e })
}

func isKeyID(s string) bool {
	return isLowerHex(s, 16)
}

func isSHA256(s string) bool {
	return isLowerHex(s, 64)
}

func isPartCount(s string) bool {
	if len(s) < 1 || len(s) > 5 || s[0] < '1' || s[0] > '9' || !allBytes(s, isDigitByte) {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n <= maxPartCount
}

func isETag(s string) bool {
	base, parts, multipart := strings.Cut(s, "-")
	if !isLowerHex(base, 32) {
		return false
	}
	return !multipart || isPartCount(parts)
}

func isWriterNameByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || isDigitByte(c) || c == '_' || c == '-'
}

func isWriter(s string) bool {
	kind, name, ok := strings.Cut(s, ".")
	if !ok || (kind != "key" && kind != "dev" && kind != "sys") {
		return false
	}
	return len(name) >= 1 && len(name) <= 32 && allBytes(name, isWriterNameByte)
}

func isBucketNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || isDigitByte(c) || c == '.' || c == '-'
}

func isBucketName(s string) bool {
	return len(s) >= 3 && len(s) <= 63 && allBytes(s, isBucketNameByte)
}

func isDeploymentID(s string) bool {
	return len(s) == 26 && allBytes(s, func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '2' && c <= '7' })
}

func isKEKFingerprint(s string) bool {
	rest, ok := strings.CutPrefix(s, "bkf1_")
	return ok && isLowerHex(rest, 32)
}

func isPolicy(s string) bool {
	return s == "private" || s == "public-read"
}

func isDefaultEncryption(s string) bool {
	return s == "none" || s == "tde1"
}

func isMetaNameByte(c byte) bool {
	if c >= 'a' && c <= 'z' || isDigitByte(c) {
		return true
	}
	return strings.IndexByte("!#$%&'*+.^_`|~-", c) >= 0
}

func isMetaName(s string) bool {
	return len(s) >= 1 && allBytes(s, isMetaNameByte)
}

func isMetaValue(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	return allBytes(s, func(c byte) bool { return c >= 0x20 && c != 0x7f || c == 0x09 })
}

func isSystemHeaderName(s string) bool {
	switch s {
	case "cache-control", "content-disposition", "content-encoding", "content-language", "expires":
		return true
	}
	return false
}

func userMetaAccepted(name, value string) bool {
	return isMetaName(name) && isMetaValue(value)
}

func systemMetaAccepted(name, value string) bool {
	return isSystemHeaderName(name) && len(value) >= 1 && len(value) <= MaxSysHeaderBytes && isMetaValue(value)
}

func userMetaWithinLimit(entries map[string]string) bool {
	total := 0
	for name, value := range entries {
		if !userMetaAccepted(name, value) {
			return false
		}
		total += len(name) + len(value)
	}
	return total <= MaxUserMetaBytes
}

func systemMetaWithinLimit(entries map[string]string) bool {
	for name, value := range entries {
		if !systemMetaAccepted(name, value) {
			return false
		}
	}
	return true
}

func parseInt(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, ErrWireMalformed
	}
	return n, nil
}

func setInt64Range(dst *int64, raw string, lowest, highest int64) error {
	n, err := parseInt(raw)
	if err != nil {
		return err
	}
	if n < lowest || n > highest {
		return ErrWireMalformed
	}
	*dst = n
	return nil
}

func setMsgID(dst *int64, raw string, lowest int64) error {
	return setInt64Range(dst, raw, lowest, MaxMsgID)
}

func setPositiveInt64(dst *int64, raw string) error {
	return setInt64Range(dst, raw, 1, math.MaxInt64)
}

func setBoundedInt(dst *int, raw string, lowest, highest int64) error {
	var n int64
	if err := setInt64Range(&n, raw, lowest, highest); err != nil {
		return err
	}
	*dst = int(n)
	return nil
}

func setJobID(dst *string, raw string) error {
	if !isJobID(raw) {
		return ErrWireMalformed
	}
	*dst = raw
	return nil
}

func setPattern(dst *string, raw string, matches func(string) bool) error {
	if !matches(raw) {
		return ErrWireMalformed
	}
	*dst = raw
	return nil
}

func setKey(dst *string, raw string) error {
	if raw == "" {
		return ErrWireMalformed
	}
	key, err := url.QueryUnescape(raw)
	if err != nil || !isKey(key) {
		return ErrWireMalformed
	}
	*dst = key
	return nil
}

func setContentType(dst *string, raw string) error {
	value, err := url.QueryUnescape(raw)
	if err != nil || !isContentType(value) {
		return ErrWireMalformed
	}
	*dst = value
	return nil
}

func setB64Len(dst *[]byte, raw string, length int) error {
	if strings.ContainsAny(raw, "\r\n") {
		return ErrWireMalformed
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != length {
		return ErrWireMalformed
	}
	*dst = decoded
	return nil
}

func setMetaEntry(dst map[string]string, rawName, rawValue string, accept func(name, value string) bool) error {
	if rawName == "" {
		return ErrWireMalformed
	}
	name, err := url.QueryUnescape(rawName)
	if err != nil {
		return ErrWireMalformed
	}
	value, err := url.QueryUnescape(rawValue)
	if err != nil || !accept(name, value) {
		return ErrWireMalformed
	}
	if _, duplicate := dst[name]; duplicate {
		return ErrWireMalformed
	}
	dst[name] = value
	return nil
}

func collectMetaEntries(pairs map[string]string, prefix string, accept func(name, value string) bool) (map[string]string, error) {
	var entries map[string]string
	for key, rawValue := range pairs {
		rawName, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if entries == nil {
			entries = make(map[string]string)
		}
		if err := setMetaEntry(entries, rawName, rawValue, accept); err != nil {
			return nil, err
		}
	}
	return entries, nil
}
