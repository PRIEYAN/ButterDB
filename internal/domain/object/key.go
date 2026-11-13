package object

import (
	"fmt"
	"unicode/utf8"
)

const (
	MaxKeyBytes          = 1024
	MaxUserMetadataBytes = 2048
)

func ParseKey(raw string) (Key, error) {
	key := Key(raw)
	if err := key.Validate(); err != nil {
		return "", err
	}
	return key, nil
}

func (key Key) String() string {
	return string(key)
}

func (key Key) Validate() error {
	if len(key) == 0 {
		return fmt.Errorf("object: key: %w: %w", ErrInvalidKey, ErrKeyEmpty)
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("object: key of %d bytes: %w: %w", len(key), ErrInvalidKey, ErrKeyTooLong)
	}
	if !utf8.ValidString(string(key)) {
		return fmt.Errorf("object: key: %w: %w", ErrInvalidKey, ErrKeyNotUTF8)
	}
	for _, r := range string(key) {
		if isControlCharacter(r) {
			return fmt.Errorf("object: key: %w: %w", ErrInvalidKey, ErrKeyControlCharacter)
		}
	}
	return nil
}

func isControlCharacter(r rune) bool {
	return r <= 0x1f || r == 0x7f
}
