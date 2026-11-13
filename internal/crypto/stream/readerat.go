package stream

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const chunkStride = format.ChunkSize + format.TagLen

var ErrReaderClosed = errors.New("stream: encrypting reader is closed")

type EncryptingReaderAt struct {
	mu            sync.RWMutex
	source        io.ReaderAt
	plaintextSize int64
	header        [format.HeaderLen]byte
	noncePrefix   [format.NoncePrefixLen]byte
	subkey        []byte
}

func NewEncryptingReaderAt(source io.ReaderAt, plaintextSize int64, dek []byte, fileSalt [format.SaltLen]byte, noncePrefix [format.NoncePrefixLen]byte) (*EncryptingReaderAt, error) {
	if source == nil {
		return nil, fmt.Errorf("stream: %w: plaintext source", errs.ErrMissingDependency)
	}
	header, err := format.EncodeHeader(format.Header{Salt: fileSalt, NoncePrefix: noncePrefix, PlaintextSize: plaintextSize})
	if err != nil {
		return nil, fmt.Errorf("stream: encode header: %w", err)
	}
	subkey, err := DeriveSubkey(dek, fileSalt)
	if err != nil {
		return nil, err
	}
	return &EncryptingReaderAt{
		source:        source,
		plaintextSize: plaintextSize,
		header:        header,
		noncePrefix:   noncePrefix,
		subkey:        subkey,
	}, nil
}

func (r *EncryptingReaderAt) Size() int64 {
	return format.CiphertextSize(r.plaintextSize)
}

func (r *EncryptingReaderAt) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.subkey)
	r.subkey = nil
	r.source = nil
	return nil
}

func (r *EncryptingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("stream: negative read offset")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.subkey == nil {
		return 0, ErrReaderClosed
	}
	total := r.Size()
	if off >= total {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	want := len(p)
	if remaining := total - off; int64(want) > remaining {
		want = int(remaining)
	}
	done, err := r.fill(p[:want], off)
	if err != nil {
		return done, err
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

func (r *EncryptingReaderAt) fill(dst []byte, off int64) (int, error) {
	done := 0
	if off < format.HeaderLen {
		done = copy(dst, r.header[off:])
	}
	if done == len(dst) {
		return done, nil
	}
	aead, err := chacha20poly1305.NewX(r.subkey)
	if err != nil {
		return done, fmt.Errorf("stream: new aead: %w", err)
	}
	buf := make([]byte, chunkStride)
	defer clear(buf)
	for done < len(dst) {
		relative := off + int64(done) - format.HeaderLen
		index := relative / chunkStride
		within := relative % chunkStride
		sealed, err := r.sealChunk(aead, buf, index)
		if err != nil {
			return done, err
		}
		done += copy(dst[done:], sealed[within:])
	}
	return done, nil
}

func (r *EncryptingReaderAt) sealChunk(aead cipher.AEAD, buf []byte, index int64) ([]byte, error) {
	finalIndex := r.plaintextSize / format.ChunkSize
	length := int64(format.ChunkSize)
	if index == finalIndex {
		length = r.plaintextSize % format.ChunkSize
	}
	if length > 0 {
		n, err := r.source.ReadAt(buf[:length], index*format.ChunkSize)
		if int64(n) != length {
			if err == nil || errors.Is(err, io.EOF) {
				return nil, ErrPlaintextSizeMismatch
			}
			return nil, fmt.Errorf("stream: read plaintext: %w", err)
		}
	}
	nonce := format.BuildNonce(r.noncePrefix, uint32(index), index == finalIndex)
	defer clear(nonce[:])
	return aead.Seal(buf[:0], nonce[:], buf[:length], nil), nil
}
