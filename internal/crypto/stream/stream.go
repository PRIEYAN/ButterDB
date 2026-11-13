package stream

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
)

func EncryptStream(dst io.Writer, src io.Reader, dek []byte, plaintextSize int64, fileSalt [format.SaltLen]byte, noncePrefix [format.NoncePrefixLen]byte) error {
	if len(dek) != KeyLen {
		return ErrInvalidMasterKey
	}
	header, err := format.EncodeHeader(format.Header{Salt: fileSalt, NoncePrefix: noncePrefix, PlaintextSize: plaintextSize})
	if err != nil {
		return fmt.Errorf("stream: encode header: %w", err)
	}
	subkey, err := DeriveSubkey(dek, fileSalt)
	if err != nil {
		return err
	}
	defer clear(subkey)
	aead, err := chacha20poly1305.NewX(subkey)
	if err != nil {
		return fmt.Errorf("stream: new aead: %w", err)
	}
	if err := writeExact(dst, header[:]); err != nil {
		return err
	}
	return sealChunks(dst, src, aead, noncePrefix, plaintextSize)
}

func sealChunks(dst io.Writer, src io.Reader, aead cipher.AEAD, noncePrefix [format.NoncePrefixLen]byte, plaintextSize int64) error {
	buf := make([]byte, format.ChunkSize+format.TagLen)
	defer clear(buf)
	var counter uint32
	remaining := plaintextSize
	for remaining > 0 {
		want := format.ChunkSize
		if remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := io.ReadFull(src, buf[:want])
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				return ErrPlaintextSizeMismatch
			}
			return fmt.Errorf("stream: read plaintext: %w", readErr)
		}
		remaining -= int64(n)
		isFinal := remaining == 0 && n < format.ChunkSize
		if isFinal {
			if err := ensureNoExtraPlaintext(src); err != nil {
				return err
			}
		}
		if err := sealAndWrite(dst, aead, buf, n, noncePrefix, counter, isFinal); err != nil {
			return err
		}
		counter++
		if isFinal {
			return nil
		}
	}
	if err := ensureNoExtraPlaintext(src); err != nil {
		return err
	}
	return sealAndWrite(dst, aead, buf, 0, noncePrefix, counter, true)
}

func sealAndWrite(dst io.Writer, aead cipher.AEAD, buf []byte, plaintextLen int, noncePrefix [format.NoncePrefixLen]byte, counter uint32, final bool) error {
	nonce := format.BuildNonce(noncePrefix, counter, final)
	defer clear(nonce[:])
	sealed := aead.Seal(buf[:0], nonce[:], buf[:plaintextLen], nil)
	return writeExact(dst, sealed)
}

func DecryptStream(dst io.Writer, src io.Reader, dek []byte) (int64, error) {
	if len(dek) != KeyLen {
		return 0, ErrInvalidMasterKey
	}
	raw := make([]byte, format.HeaderLen)
	defer clear(raw)
	if _, err := io.ReadFull(src, raw); err != nil {
		return 0, format.ErrShortHeader
	}
	header, err := ParseHeader(raw)
	if err != nil {
		return 0, err
	}
	subkey, err := DeriveSubkey(dek, header.Salt)
	if err != nil {
		return 0, err
	}
	defer clear(subkey)
	aead, err := chacha20poly1305.NewX(subkey)
	if err != nil {
		return 0, fmt.Errorf("stream: new aead: %w", err)
	}

	buf := make([]byte, format.ChunkSize+format.TagLen)
	defer clear(buf)
	var counter uint32
	var written int64
	for {
		n, readErr := io.ReadFull(src, buf)
		if readErr == io.EOF {
			return 0, ErrAuthFailed
		}
		isFinal := readErr == io.ErrUnexpectedEOF
		if !isFinal && readErr != nil {
			return 0, fmt.Errorf("stream: read ciphertext: %w", readErr)
		}
		nonce := format.BuildNonce(header.NoncePrefix, counter, isFinal)
		plaintext, openErr := aead.Open(buf[:0], nonce[:], buf[:n], nil)
		clear(nonce[:])
		if openErr != nil {
			return 0, ErrAuthFailed
		}
		plaintextLength := len(plaintext)
		writeErr := writeExact(dst, plaintext)
		clear(plaintext)
		if writeErr != nil {
			return 0, writeErr
		}
		written += int64(plaintextLength)
		counter++
		if isFinal {
			if written != header.PlaintextSize {
				return 0, ErrAuthFailed
			}
			return header.PlaintextSize, nil
		}
	}
}

func ensureNoExtraPlaintext(src io.Reader) error {
	var extra [1]byte
	defer clear(extra[:])
	for emptyReads := 0; emptyReads < maxConsecutiveEmptyReads; emptyReads++ {
		n, err := src.Read(extra[:])
		if n > 0 {
			return ErrPlaintextSizeMismatch
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stream: read plaintext: %w", err)
		}
	}
	return io.ErrNoProgress
}

func writeExact(dst io.Writer, buffer []byte) error {
	written, err := dst.Write(buffer)
	if err != nil {
		return fmt.Errorf("stream: write: %w", err)
	}
	if written != len(buffer) {
		return io.ErrShortWrite
	}
	return nil
}
