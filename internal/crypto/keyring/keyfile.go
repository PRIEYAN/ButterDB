package keyring

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func readKeyFile(path string) ([keySize]byte, error) {
	var out [keySize]byte
	file, err := os.Open(path)
	if err != nil {
		return out, fmt.Errorf("keyring: open key file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return out, fmt.Errorf("keyring: stat key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return out, ErrKeyFileNotRegular
	}
	if err := checkOwnership(info); err != nil {
		return out, err
	}
	buf := make([]byte, keySize+1)
	defer clear(buf)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("keyring: read key file: %w", err)
	}
	if n != keySize {
		return out, ErrKeyFileLength
	}
	copy(out[:], buf[:keySize])
	return out, nil
}
