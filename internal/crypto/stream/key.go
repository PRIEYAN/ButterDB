package stream

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	hkdfInfo                 = "butterDB/file/v1"
	maxConsecutiveEmptyReads = 100
	KeyLen                   = 32
)

var (
	ErrAuthFailed            = errors.New("stream: ciphertext failed authentication")
	ErrPlaintextSizeMismatch = errors.New("stream: plaintext size does not match declared length")
	ErrInvalidMasterKey      = errors.New("stream: key must be 32 bytes")
)

type FileParams struct {
	Salt        [format.SaltLen]byte
	NoncePrefix [format.NoncePrefixLen]byte
}

func NewFileParams(random io.Reader) (FileParams, error) {
	var params FileParams
	if random == nil {
		return params, fmt.Errorf("stream: %w: random source", errs.ErrMissingDependency)
	}
	if _, err := io.ReadFull(random, params.Salt[:]); err != nil {
		return FileParams{}, fmt.Errorf("stream: draw file salt: %w", err)
	}
	if _, err := io.ReadFull(random, params.NoncePrefix[:]); err != nil {
		return FileParams{}, fmt.Errorf("stream: draw nonce prefix: %w", err)
	}
	return params, nil
}

func DeriveSubkey(dek []byte, salt [format.SaltLen]byte) ([]byte, error) {
	if len(dek) != KeyLen {
		return nil, ErrInvalidMasterKey
	}
	subkey, err := hkdf.Key(sha256.New, dek, salt[:], hkdfInfo, KeyLen)
	if err != nil {
		return nil, fmt.Errorf("stream: derive file key: %w", err)
	}
	return subkey, nil
}

func ParseHeader(raw []byte) (format.Header, error) {
	header, err := format.DecodeHeader(raw)
	if err == nil {
		return header, nil
	}
	if errors.Is(err, format.ErrPlaintextTooLarge) {
		return format.Header{}, format.ErrUnsupported
	}
	return format.Header{}, err
}
