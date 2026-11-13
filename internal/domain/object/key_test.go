package object

import (
	"errors"
	"strings"
	"testing"
)

func TestKeyValidation(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr error
	}{
		{name: "one byte", key: "a"},
		{name: "path like", key: "2026/11/cat.jpg"},
		{name: "trailing slash", key: "2026/11/"},
		{name: "unicode", key: "photos/日本語.png"},
		{name: "space and punctuation", key: "a b+c=d&e"},
		{name: "exactly 1024 bytes", key: strings.Repeat("k", 1024)},
		{name: "1024 bytes multibyte", key: strings.Repeat("é", 512)},
		{name: "empty", key: "", wantErr: ErrKeyEmpty},
		{name: "1025 bytes", key: strings.Repeat("k", 1025), wantErr: ErrKeyTooLong},
		{name: "1025 bytes multibyte", key: strings.Repeat("é", 512) + "k", wantErr: ErrKeyTooLong},
		{name: "nul", key: "a\x00b", wantErr: ErrKeyControlCharacter},
		{name: "tab", key: "a\tb", wantErr: ErrKeyControlCharacter},
		{name: "newline", key: "a\nb", wantErr: ErrKeyControlCharacter},
		{name: "unit separator", key: "a\x1fb", wantErr: ErrKeyControlCharacter},
		{name: "delete character", key: "a\x7fb", wantErr: ErrKeyControlCharacter},
		{name: "invalid utf-8 lone continuation", key: "a\x80b", wantErr: ErrKeyNotUTF8},
		{name: "invalid utf-8 truncated sequence", key: "a\xe2\x82", wantErr: ErrKeyNotUTF8},
		{name: "invalid utf-8 overlong", key: "\xc0\xaf", wantErr: ErrKeyNotUTF8},
		{name: "invalid utf-8 surrogate", key: "\xed\xa0\x80", wantErr: ErrKeyNotUTF8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, err := ParseKey(test.key)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("ParseKey() error = %v", err)
				}
				if key.String() != test.key {
					t.Fatalf("ParseKey() = %q, want %q", key, test.key)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ParseKey() error = %v, want %v", err, test.wantErr)
			}
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("ParseKey() error = %v, want ErrInvalidKey", err)
			}
			if key != "" {
				t.Fatalf("ParseKey() = %q on error", key)
			}
		})
	}
}

func TestUserMetadataLimit(t *testing.T) {
	tests := []struct {
		name    string
		meta    map[string]string
		wantErr error
	}{
		{name: "nil", meta: nil},
		{name: "empty", meta: map[string]string{}},
		{name: "one entry", meta: map[string]string{"camera": "x100v"}},
		{name: "empty value", meta: map[string]string{"flag": ""}},
		{name: "exactly 2048 bytes", meta: map[string]string{"k": strings.Repeat("v", 2047)}},
		{name: "2049 bytes", meta: map[string]string{"k": strings.Repeat("v", 2048)}, wantErr: ErrMetadataTooLarge},
		{name: "sum across entries at limit", meta: map[string]string{"aa": strings.Repeat("v", 1022), "bb": strings.Repeat("w", 1022)}},
		{name: "sum across entries over limit", meta: map[string]string{"aa": strings.Repeat("v", 1023), "bb": strings.Repeat("w", 1022)}, wantErr: ErrMetadataTooLarge},
		{name: "multibyte counts bytes", meta: map[string]string{"k": strings.Repeat("é", 1024)}, wantErr: ErrMetadataTooLarge},
		{name: "empty name", meta: map[string]string{"": "v"}, wantErr: ErrInvalidMetadataName},
		{name: "name with space", meta: map[string]string{"a b": "v"}, wantErr: ErrInvalidMetadataName},
		{name: "name with colon", meta: map[string]string{"a:b": "v"}, wantErr: ErrInvalidMetadataName},
		{name: "name with non ascii", meta: map[string]string{"café": "v"}, wantErr: ErrInvalidMetadataName},
		{name: "token punctuation name", meta: map[string]string{"a-b_c.d": "v"}},
		{name: "invalid utf-8 value", meta: map[string]string{"k": "\xff"}, wantErr: ErrInvalidMetadataText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateUserMeta(test.meta)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateUserMeta() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestUserMetaBytesAndNames(t *testing.T) {
	if got := UserMetaBytes(map[string]string{"ab": "cde", "f": "g"}); got != 7 {
		t.Fatalf("UserMetaBytes() = %d, want 7", got)
	}
	if got := NormalizeUserMetaName("Camera-ID"); got != "camera-id" {
		t.Fatalf("NormalizeUserMetaName() = %q", got)
	}
}
