package keyring

import (
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var (
	ErrDEKUnwrap = fmt.Errorf("keyring: dek unwrap failed: %w", errs.ErrKeyUnavailable)
	ErrDEKCheck  = fmt.Errorf("keyring: dek key check failed: %w", errs.ErrKeyUnavailable)

	ErrSecretUnseal = fmt.Errorf("keyring: secret unseal failed: %w", errs.ErrKeyUnavailable)

	ErrKeyFileLength      = errors.New("keyring: key file must be exactly 32 bytes")
	ErrKeyFilePermissions = errors.New("keyring: key file permissions are not acceptable")
	ErrKeyFileNotRegular  = errors.New("keyring: key file is not a regular file")
	ErrKeyFileExists      = errors.New("keyring: key file already exists")
	ErrKeyLength          = errors.New("keyring: key must be exactly 32 bytes")
	ErrInvalidKeyID       = errors.New("keyring: key id must be 16 lowercase hex characters")
	ErrEmptySecret        = errors.New("keyring: secret is empty")
	ErrNilKey             = errors.New("keyring: key is required")
	ErrNilRandom          = errors.New("keyring: random source is required")
	ErrRingClosed         = errors.New("keyring: ring is closed")
)
