package bucket

import "fmt"

const (
	PolicyPrivate    = "private"
	PolicyPublicRead = "public-read"

	EncryptionNone = "none"
	EncryptionTDE1 = "tde1"

	MaxCacheMaxAgeSeconds     = 31536000
	DefaultCacheMaxAgeSeconds = 3600
)

func ValidatePolicy(policy string) error {
	if policy != PolicyPrivate && policy != PolicyPublicRead {
		return fmt.Errorf("bucket: policy: %w", ErrInvalidPolicy)
	}
	return nil
}

func ValidateDefaultEncryption(mode string) error {
	if mode != EncryptionNone && mode != EncryptionTDE1 {
		return fmt.Errorf("bucket: default encryption: %w", ErrInvalidEncryption)
	}
	return nil
}

func ValidateCacheMaxAge(seconds int64) error {
	if seconds < 0 || seconds > MaxCacheMaxAgeSeconds {
		return fmt.Errorf("bucket: cache max age: %w", ErrInvalidCacheMaxAge)
	}
	return nil
}

func NewSpec(name, policy, defaultEncryption string, cacheMaxAge int64) (Spec, error) {
	spec := Spec{
		Name:              BucketName(name),
		Policy:            policy,
		DefaultEncryption: defaultEncryption,
		CacheMaxAge:       cacheMaxAge,
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func (spec Spec) Validate() error {
	if err := spec.Name.Validate(); err != nil {
		return err
	}
	if err := ValidatePolicy(spec.Policy); err != nil {
		return err
	}
	if err := ValidateDefaultEncryption(spec.DefaultEncryption); err != nil {
		return err
	}
	return ValidateCacheMaxAge(spec.CacheMaxAge)
}

func (patch Patch) Validate() error {
	if patch.Policy != nil {
		if err := ValidatePolicy(*patch.Policy); err != nil {
			return err
		}
	}
	if patch.DefaultEncryption != nil {
		if err := ValidateDefaultEncryption(*patch.DefaultEncryption); err != nil {
			return err
		}
	}
	if patch.CacheMaxAge != nil {
		return ValidateCacheMaxAge(*patch.CacheMaxAge)
	}
	return nil
}

func (patch Patch) IsEmpty() bool {
	return patch.Policy == nil && patch.DefaultEncryption == nil && patch.CacheMaxAge == nil
}
