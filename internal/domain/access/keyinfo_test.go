package access

import (
	"errors"
	"strings"
	"testing"
)

func TestKeyNameValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "one character", input: "a", valid: true},
		{name: "spec example", input: "website prod", valid: true},
		{name: "64 characters", input: strings.Repeat("a", 64), valid: true},
		{name: "64 multibyte characters", input: strings.Repeat("é", 64), valid: true},
		{name: "empty", input: ""},
		{name: "65 characters", input: strings.Repeat("a", 65)},
		{name: "65 multibyte characters", input: strings.Repeat("é", 65)},
		{name: "invalid utf-8", input: "bad\xff"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateKeyName(test.input)
			if test.valid {
				if err != nil {
					t.Fatalf("ValidateKeyName(%q) = %v, want nil", test.input, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidKeyName) {
				t.Fatalf("ValidateKeyName(%q) = %v, want ErrInvalidKeyName", test.input, err)
			}
		})
	}
}

func TestKeyInfoRevoked(t *testing.T) {
	tests := []struct {
		name      string
		revokedAt int64
		want      bool
	}{
		{name: "active", revokedAt: 0, want: false},
		{name: "revoked", revokedAt: 1700000000, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := (KeyInfo{RevokedAt: test.revokedAt}).Revoked(); got != test.want {
				t.Fatalf("Revoked() = %v, want %v", got, test.want)
			}
		})
	}
}
