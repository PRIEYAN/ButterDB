package access

import (
	"fmt"
	"unicode/utf8"
)

const (
	MinKeyNameLength = 1
	MaxKeyNameLength = 64
)

type KeyInfo struct {
	ID         AccessKeyID
	Name       string
	Scope      Scope
	CreatedAt  int64
	UpdatedAt  int64
	LastUsedAt int64
	RevokedAt  int64
}

func (info KeyInfo) Revoked() bool {
	return info.RevokedAt != 0
}

func ValidateKeyName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("access: key name: %w: not valid utf-8", ErrInvalidKeyName)
	}
	count := utf8.RuneCountInString(name)
	if count < MinKeyNameLength || count > MaxKeyNameLength {
		return fmt.Errorf("access: key name: %w: length must be %d to %d characters", ErrInvalidKeyName, MinKeyNameLength, MaxKeyNameLength)
	}
	return nil
}
