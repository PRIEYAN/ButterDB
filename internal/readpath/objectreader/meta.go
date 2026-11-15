package objectreader

import (
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const supportedEncryptionVersion = 1

func validateMeta(meta object.ObjectMeta) error {
	if meta.PlaintextSize < 0 || meta.StoredSize < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalidMeta)
	}
	if meta.BodyMsgID < 0 {
		return fmt.Errorf("%w: negative body message id", ErrInvalidMeta)
	}
	if meta.Encrypted {
		return validateEncryptedMeta(meta)
	}
	if meta.PlaintextSize != meta.StoredSize {
		return fmt.Errorf("%w: plaintext size %d does not match stored size %d", ErrInvalidMeta, meta.PlaintextSize, meta.StoredSize)
	}
	if meta.BodyMsgID == 0 && meta.StoredSize != 0 {
		return fmt.Errorf("%w: object has bytes but no body message", ErrInvalidMeta)
	}
	return nil
}

func validateEncryptedMeta(meta object.ObjectMeta) error {
	if meta.EncryptionVersion != supportedEncryptionVersion {
		return fmt.Errorf("%w: version %d", ErrEncryptedUnsupported, meta.EncryptionVersion)
	}
	if err := format.ValidatePlaintextSize(meta.PlaintextSize); err != nil {
		return fmt.Errorf("objectreader: invalid encrypted plaintext size: %w", err)
	}
	if format.CiphertextSize(meta.PlaintextSize) != meta.StoredSize {
		return fmt.Errorf("objectreader: encrypted stored size %d does not match plaintext size %d: %w", meta.StoredSize, meta.PlaintextSize, randomaccess.ErrCiphertextSize)
	}
	if meta.BodyMsgID == 0 {
		return fmt.Errorf("%w: encrypted object has no body message", ErrInvalidMeta)
	}
	if meta.KeyID == "" {
		return fmt.Errorf("%w: encrypted object has no key id", ErrInvalidMeta)
	}
	return nil
}

func pinnedMeta(meta object.ObjectMeta) object.ObjectMeta {
	meta.UserMeta = nil
	meta.SystemMeta = nil
	return meta
}
