package keyring

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
)

const (
	escrowAADPrefix = "butterdb/escrow/v1|"
	escrowSaltBytes = 16
	escrowWrapBytes = chacha20poly1305.NonceSizeX + keySize + chacha20poly1305.Overhead
)

var ErrEscrowPassword = errors.New("keyring: escrow password does not open the key")

type EscrowParams struct {
	Time      int
	MemoryKiB int
	Parallel  int
}

var DefaultEscrowParams = EscrowParams{Time: 3, MemoryKiB: 65536, Parallel: 4}

type Escrow struct {
	Params      EscrowParams
	Salt        []byte
	Wrap        []byte
	Fingerprint string
}

func escrowAAD(deploymentID, fingerprint string) []byte {
	return []byte(escrowAADPrefix + deploymentID + "|" + fingerprint)
}

func escrowKey(password, salt []byte, p EscrowParams) []byte {
	return argon2.IDKey(password, salt, uint32(p.Time), uint32(p.MemoryKiB), uint8(p.Parallel), keySize)
}

func fingerprintOf(root []byte) (string, error) {
	fp, err := deriveBytes(root, infoFingerprint)
	if err != nil {
		return "", err
	}
	defer clear(fp)
	return fingerprintPrefix + hex.EncodeToString(fp[:fingerprintBytes]), nil
}

func SealEscrowFile(keyPath string, password []byte, deploymentID string, params EscrowParams, rand io.Reader) (Escrow, error) {
	if rand == nil {
		return Escrow{}, ErrNilRandom
	}
	root, err := readKeyFile(keyPath)
	if err != nil {
		return Escrow{}, err
	}
	defer clear(root[:])
	fingerprint, err := fingerprintOf(root[:])
	if err != nil {
		return Escrow{}, err
	}
	salt := make([]byte, escrowSaltBytes)
	if _, err := io.ReadFull(rand, salt); err != nil {
		return Escrow{}, fmt.Errorf("keyring: escrow salt: %w", err)
	}
	kek := escrowKey(password, salt, params)
	defer clear(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return Escrow{}, fmt.Errorf("keyring: escrow cipher: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX, escrowWrapBytes)
	if _, err := io.ReadFull(rand, nonce); err != nil {
		return Escrow{}, fmt.Errorf("keyring: escrow nonce: %w", err)
	}
	wrap := aead.Seal(nonce, nonce, root[:], escrowAAD(deploymentID, fingerprint))
	return Escrow{Params: params, Salt: salt, Wrap: wrap, Fingerprint: fingerprint}, nil
}

func openEscrow(e Escrow, password []byte, deploymentID string) ([]byte, error) {
	if len(e.Salt) != escrowSaltBytes || len(e.Wrap) != escrowWrapBytes {
		return nil, ErrEscrowPassword
	}
	kek := escrowKey(password, e.Salt, e.Params)
	defer clear(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: escrow cipher: %w", err)
	}
	nonce, sealed := e.Wrap[:chacha20poly1305.NonceSizeX], e.Wrap[chacha20poly1305.NonceSizeX:]
	root, err := aead.Open(nil, nonce, sealed, escrowAAD(deploymentID, e.Fingerprint))
	if err != nil {
		return nil, ErrEscrowPassword
	}
	fingerprint, err := fingerprintOf(root)
	if err != nil {
		clear(root)
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(e.Fingerprint)) != 1 {
		clear(root)
		return nil, ErrEscrowPassword
	}
	return root, nil
}

func VerifyEscrow(e Escrow, password []byte, deploymentID string) error {
	root, err := openEscrow(e, password, deploymentID)
	if err != nil {
		return err
	}
	clear(root)
	return nil
}

func OpenEscrowToFile(e Escrow, password []byte, deploymentID, keyPath string) error {
	root, err := openEscrow(e, password, deploymentID)
	if err != nil {
		return err
	}
	defer clear(root)
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		return fmt.Errorf("keyring: create secret writer: %w", err)
	}
	if err := writer.Write(keyPath, root); err != nil {
		return fmt.Errorf("keyring: write key file: %w", err)
	}
	return nil
}

func KeyFileFingerprint(keyPath string) (string, error) {
	root, err := readKeyFile(keyPath)
	if err != nil {
		return "", err
	}
	defer clear(root[:])
	return fingerprintOf(root[:])
}
