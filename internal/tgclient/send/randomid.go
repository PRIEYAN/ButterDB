package send

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxIdempotencyKeyBytes = 256

var ErrInvalidIdempotencyKey = errors.New("send: invalid idempotency key")

func StableRandomID(jobID, purpose string) (int64, error) {
	if err := validateIdempotencyKey("job id", jobID); err != nil {
		return 0, err
	}
	if err := validateIdempotencyKey("purpose", purpose); err != nil {
		return 0, err
	}
	digest := sha256.Sum256([]byte(jobID + "\x00" + purpose))
	return int64(binary.LittleEndian.Uint64(digest[:8])) | 1, nil
}

func validateIdempotencyKey(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s must be non-empty without surrounding whitespace", ErrInvalidIdempotencyKey, label)
	}
	if len(value) > maxIdempotencyKeyBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidIdempotencyKey, label, maxIdempotencyKeyBytes)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: %s contains invalid text", ErrInvalidIdempotencyKey, label)
	}
	return nil
}
