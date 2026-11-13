package keyring

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
)

func Generate(path string, rand io.Reader) (_ [16]byte, retErr error) {
	var zero [fingerprintBytes]byte
	if rand == nil {
		return zero, ErrNilRandom
	}
	fsys := fsx.OS{}
	if _, err := fsys.Stat(path); err == nil {
		return zero, ErrKeyFileExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return zero, fmt.Errorf("keyring: inspect key file path: %w", err)
	}
	writer, err := secretfile.New(fsys)
	if err != nil {
		return zero, fmt.Errorf("keyring: create secret writer: %w", err)
	}
	raw := make([]byte, keySize)
	defer clear(raw)
	if _, err := io.ReadFull(rand, raw); err != nil {
		return zero, fmt.Errorf("keyring: read server key bytes: %w", err)
	}
	if err := writer.Write(path, raw); err != nil {
		return zero, fmt.Errorf("keyring: write key file: %w", err)
	}
	fp, err := deriveBytes(raw, infoFingerprint)
	if err != nil {
		return zero, err
	}
	defer clear(fp)
	var out [fingerprintBytes]byte
	copy(out[:], fp[:fingerprintBytes])
	return out, nil
}
