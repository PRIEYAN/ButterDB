package object

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func ValidateUserMeta(meta map[string]string) error {
	total := 0
	for name, value := range meta {
		if !isHTTPToken(name) {
			return fmt.Errorf("object: user metadata: %w", ErrInvalidMetadataName)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("object: user metadata: %w", ErrInvalidMetadataText)
		}
		total += len(name) + len(value)
	}
	if total > MaxUserMetadataBytes {
		return fmt.Errorf("object: user metadata of %d bytes: %w", total, ErrMetadataTooLarge)
	}
	return nil
}

func UserMetaBytes(meta map[string]string) int {
	total := 0
	for name, value := range meta {
		total += len(name) + len(value)
	}
	return total
}

func NormalizeUserMetaName(name string) string {
	return strings.ToLower(name)
}

func isHTTPToken(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isTokenByte(name[i]) {
			return false
		}
	}
	return true
}

func isTokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0
}
