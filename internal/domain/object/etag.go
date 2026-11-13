package object

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	md5HexLength      = 32
	maxMultipartParts = 10000
)

func SingleETag(md5Sum [16]byte) string {
	return hex.EncodeToString(md5Sum[:])
}

func MultipartETag(partMD5s [][16]byte) (string, error) {
	if len(partMD5s) == 0 {
		return "", fmt.Errorf("object: multipart etag: %w", ErrNoParts)
	}
	if len(partMD5s) > maxMultipartParts {
		return "", fmt.Errorf("object: multipart etag of %d parts: %w", len(partMD5s), ErrTooManyParts)
	}
	joined := make([]byte, 0, len(partMD5s)*16)
	for _, sum := range partMD5s {
		joined = append(joined, sum[:]...)
	}
	return md5Hex(joined) + "-" + strconv.Itoa(len(partMD5s)), nil
}

func ValidateETag(etag string) error {
	digest, count, multipart := strings.Cut(etag, "-")
	if !isLowerMD5Hex(digest) {
		return fmt.Errorf("object: etag: %w", ErrInvalidETag)
	}
	if !multipart {
		return nil
	}
	if !isPartCount(count) {
		return fmt.Errorf("object: etag: %w", ErrInvalidETag)
	}
	return nil
}

func QuoteETag(etag string) string {
	return `"` + etag + `"`
}

func UnquoteETag(quoted string) (string, error) {
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return "", fmt.Errorf("object: etag: %w", ErrInvalidETag)
	}
	etag := quoted[1 : len(quoted)-1]
	if err := ValidateETag(etag); err != nil {
		return "", err
	}
	return etag, nil
}

func IsMultipartETag(etag string) bool {
	return ValidateETag(etag) == nil && strings.Contains(etag, "-")
}

func isLowerMD5Hex(digest string) bool {
	if len(digest) != md5HexLength {
		return false
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isPartCount(count string) bool {
	if count == "" || count[0] == '0' || len(count) > 5 {
		return false
	}
	for i := 0; i < len(count); i++ {
		if count[i] < '0' || count[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(count)
	return err == nil && n >= 1 && n <= maxMultipartParts
}
