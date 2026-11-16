package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

const (
	pemTypePrivateKey  = "PRIVATE KEY"
	pemTypeCertificate = "CERTIFICATE"
	maxPEMFileBytes    = 64 * 1024
	groupOtherBits     = fs.FileMode(0o077)
)

var (
	ErrKeyFilePermissions = errors.New("tlscert: key file is accessible to group or other")
	ErrInvalidKeyFile     = errors.New("tlscert: key file is not an ECDSA P-256 PKCS#8 PEM key")
)

func (s *store) loadKey() (*ecdsa.PrivateKey, error) {
	info, err := s.files.Stat(s.keyPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("tlscert: stat key file: %w", err)
	}
	if info.Mode().Perm()&groupOtherBits != 0 {
		return nil, fmt.Errorf("%w: mode %04o", ErrKeyFilePermissions, info.Mode().Perm())
	}
	data, err := s.readFile(s.keyPath)
	if err != nil {
		return nil, fmt.Errorf("tlscert: read key file: %w", err)
	}
	defer zero(data)
	return parseKey(data)
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemTypePrivateKey {
		return nil, ErrInvalidKeyFile
	}
	defer zero(block.Bytes)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalidKeyFile
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, ErrInvalidKeyFile
	}
	return key, nil
}

func (s *store) generateKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), s.random)
	if err != nil {
		return nil, fmt.Errorf("tlscert: generate key: %w", err)
	}
	return key, nil
}

func (s *store) saveKey(key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("tlscert: encode key: %w", err)
	}
	defer zero(der)
	encoded := pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: der})
	defer zero(encoded)
	if err := s.secrets.Write(s.keyPath, encoded); err != nil {
		return fmt.Errorf("tlscert: write key file: %w", err)
	}
	return nil
}

func (s *store) readFile(path string) ([]byte, error) {
	file, err := s.files.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxPEMFileBytes))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	return data, closeErr
}

func zero(data []byte) {
	clear(data)
}
