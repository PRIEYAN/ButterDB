package bucket

import (
	"errors"
	"testing"
)

func TestSpecValidation(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr error
	}{
		{name: "private none default age", spec: Spec{Name: "photos", Policy: PolicyPrivate, DefaultEncryption: EncryptionNone, CacheMaxAge: 3600}},
		{name: "public read tde1", spec: Spec{Name: "photos", Policy: PolicyPublicRead, DefaultEncryption: EncryptionTDE1, CacheMaxAge: 0}},
		{name: "max cache age", spec: Spec{Name: "photos", Policy: PolicyPrivate, DefaultEncryption: EncryptionNone, CacheMaxAge: 31536000}},
		{name: "bad name", spec: Spec{Name: "Photos", Policy: PolicyPrivate, DefaultEncryption: EncryptionNone}, wantErr: ErrInvalidBucketName},
		{name: "bad policy", spec: Spec{Name: "photos", Policy: "public", DefaultEncryption: EncryptionNone}, wantErr: ErrInvalidPolicy},
		{name: "empty policy", spec: Spec{Name: "photos", DefaultEncryption: EncryptionNone}, wantErr: ErrInvalidPolicy},
		{name: "bad encryption", spec: Spec{Name: "photos", Policy: PolicyPrivate, DefaultEncryption: "aes"}, wantErr: ErrInvalidEncryption},
		{name: "negative cache age", spec: Spec{Name: "photos", Policy: PolicyPrivate, DefaultEncryption: EncryptionNone, CacheMaxAge: -1}, wantErr: ErrInvalidCacheMaxAge},
		{name: "cache age over max", spec: Spec{Name: "photos", Policy: PolicyPrivate, DefaultEncryption: EncryptionNone, CacheMaxAge: 31536001}, wantErr: ErrInvalidCacheMaxAge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.spec.Validate(); !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
			got, err := NewSpec(string(test.spec.Name), test.spec.Policy, test.spec.DefaultEncryption, test.spec.CacheMaxAge)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewSpec() error = %v, want %v", err, test.wantErr)
			}
			if err == nil && got != test.spec {
				t.Fatalf("NewSpec() = %+v, want %+v", got, test.spec)
			}
		})
	}
}

func TestPatchValidation(t *testing.T) {
	policy := PolicyPublicRead
	badPolicy := "open"
	encryption := EncryptionTDE1
	badEncryption := "rot13"
	age := int64(60)
	badAge := int64(-5)

	tests := []struct {
		name      string
		patch     Patch
		wantErr   error
		wantEmpty bool
	}{
		{name: "empty patch", patch: Patch{}, wantEmpty: true},
		{name: "policy only", patch: Patch{Policy: &policy}},
		{name: "all fields", patch: Patch{Policy: &policy, DefaultEncryption: &encryption, CacheMaxAge: &age}},
		{name: "bad policy", patch: Patch{Policy: &badPolicy}, wantErr: ErrInvalidPolicy},
		{name: "bad encryption", patch: Patch{DefaultEncryption: &badEncryption}, wantErr: ErrInvalidEncryption},
		{name: "bad cache age", patch: Patch{CacheMaxAge: &badAge}, wantErr: ErrInvalidCacheMaxAge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.patch.Validate(); !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
			if test.patch.IsEmpty() != test.wantEmpty {
				t.Fatalf("IsEmpty() = %v, want %v", test.patch.IsEmpty(), test.wantEmpty)
			}
		})
	}
}
