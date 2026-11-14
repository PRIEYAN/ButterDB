package randomaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var (
	ErrCiphertextSize  = errors.New("randomaccess: encrypted stream size does not match its header")
	ErrDecryptorClosed = errors.New("randomaccess: decryptor is closed")
	ErrNilContext      = errors.New("randomaccess: context is required")
)

type ContextReaderAt interface {
	ReadAt(ctx context.Context, dst []byte, offset int64) (int, error)
}

type RandomAccessDecryptor struct {
	mu             sync.RWMutex
	closeOnce      sync.Once
	closed         atomic.Bool
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	source         ContextReaderAt
	plaintextSize  int64
	storedSize     int64
	noncePrefix    [format.NoncePrefixLen]byte
	subkey         []byte
}

func NewRandomAccessDecryptor(ctx context.Context, source ContextReaderAt, storedSize int64, dek []byte) (*RandomAccessDecryptor, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(dek) != stream.KeyLen {
		return nil, stream.ErrInvalidMasterKey
	}
	if source == nil {
		return nil, fmt.Errorf("randomaccess: %w: ciphertext source", errs.ErrMissingDependency)
	}
	if storedSize < format.HeaderLen {
		return nil, format.ErrShortHeader
	}
	header, err := readHeader(ctx, source, storedSize)
	if err != nil {
		return nil, err
	}
	subkey, err := stream.DeriveSubkey(dek, header.Salt)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			clear(subkey)
		}
	}()
	if err := authenticateFinalChunk(ctx, source, header, subkey); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	valid = true
	return newDecryptor(source, header.PlaintextSize, storedSize, header.NoncePrefix, subkey), nil
}

func newDecryptor(source ContextReaderAt, plaintextSize, storedSize int64, noncePrefix [format.NoncePrefixLen]byte, subkey []byte) *RandomAccessDecryptor {
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	return &RandomAccessDecryptor{
		lifetime:       lifetime,
		cancelLifetime: cancelLifetime,
		source:         source,
		plaintextSize:  plaintextSize,
		storedSize:     storedSize,
		noncePrefix:    noncePrefix,
		subkey:         subkey,
	}
}

func readHeader(ctx context.Context, source ContextReaderAt, storedSize int64) (format.Header, error) {
	raw := make([]byte, format.HeaderLen)
	defer clear(raw)
	if err := readContextAtFull(ctx, source, raw, 0); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return format.Header{}, ctxErr
		}
		return format.Header{}, format.ErrShortHeader
	}
	header, err := stream.ParseHeader(raw)
	if err != nil {
		return format.Header{}, err
	}
	if format.CiphertextSize(header.PlaintextSize) != storedSize {
		return format.Header{}, ErrCiphertextSize
	}
	return header, nil
}

func (decryptor *RandomAccessDecryptor) Clone(source ContextReaderAt) (*RandomAccessDecryptor, error) {
	if decryptor == nil {
		return nil, ErrDecryptorClosed
	}
	if source == nil {
		return nil, fmt.Errorf("randomaccess: %w: ciphertext source", errs.ErrMissingDependency)
	}
	decryptor.mu.RLock()
	defer decryptor.mu.RUnlock()
	if decryptor.closed.Load() || decryptor.lifetime == nil || len(decryptor.subkey) == 0 {
		return nil, ErrDecryptorClosed
	}
	subkey := append([]byte(nil), decryptor.subkey...)
	return newDecryptor(source, decryptor.plaintextSize, decryptor.storedSize, decryptor.noncePrefix, subkey), nil
}

func authenticateFinalChunk(ctx context.Context, source ContextReaderAt, header format.Header, subkey []byte) error {
	finalIndex := header.PlaintextSize / format.ChunkSize
	finalPlaintextSize := header.PlaintextSize % format.ChunkSize
	finalOffset, ok := format.ChunkStoredOffset(finalIndex)
	if !ok {
		return ErrCiphertextSize
	}
	ciphertext := make([]byte, finalPlaintextSize+format.TagLen)
	defer clear(ciphertext)
	if err := readContextAtFull(ctx, source, ciphertext, finalOffset); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return stream.ErrAuthFailed
	}
	aead, err := chacha20poly1305.NewX(subkey)
	if err != nil {
		return fmt.Errorf("randomaccess: new aead: %w", err)
	}
	nonce := format.BuildNonce(header.NoncePrefix, uint32(finalIndex), true)
	defer clear(nonce[:])
	plaintext, err := aead.Open(ciphertext[:0], nonce[:], ciphertext, nil)
	if err != nil || int64(len(plaintext)) != finalPlaintextSize {
		return stream.ErrAuthFailed
	}
	return nil
}

func (decryptor *RandomAccessDecryptor) Size() int64 {
	if decryptor == nil {
		return 0
	}
	return decryptor.plaintextSize
}

func (decryptor *RandomAccessDecryptor) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	if ctx == nil {
		return 0, ErrNilContext
	}
	if decryptor == nil {
		return 0, ErrDecryptorClosed
	}
	if offset < 0 {
		return 0, errors.New("randomaccess: negative read offset")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if decryptor.closed.Load() || decryptor.lifetime == nil {
		return 0, ErrDecryptorClosed
	}
	readCtx, cancelRead := context.WithCancel(ctx)
	stopLifetimeLink := context.AfterFunc(decryptor.lifetime, cancelRead)
	defer func() {
		stopLifetimeLink()
		cancelRead()
	}()
	if decryptor.lifetime.Err() != nil || decryptor.closed.Load() {
		return 0, ErrDecryptorClosed
	}
	decryptor.mu.RLock()
	defer decryptor.mu.RUnlock()
	if decryptor.closed.Load() || decryptor.source == nil || len(decryptor.subkey) == 0 {
		return 0, ErrDecryptorClosed
	}
	if offset >= decryptor.plaintextSize {
		return 0, io.EOF
	}
	if len(dst) == 0 {
		return 0, nil
	}
	want := len(dst)
	if remaining := decryptor.plaintextSize - offset; int64(want) > remaining {
		want = int(remaining)
	}
	done, err := decryptor.readChunks(readCtx, dst[:want], offset)
	if err != nil {
		return done, err
	}
	if done < len(dst) {
		return done, io.EOF
	}
	return done, nil
}

func (decryptor *RandomAccessDecryptor) readChunks(ctx context.Context, dst []byte, offset int64) (int, error) {
	aead, err := chacha20poly1305.NewX(decryptor.subkey)
	if err != nil {
		return 0, fmt.Errorf("randomaccess: new aead: %w", err)
	}
	chunkBuffer := make([]byte, format.ChunkSize+format.TagLen)
	defer clear(chunkBuffer)
	finalIndex := decryptor.plaintextSize / format.ChunkSize
	done := 0
	for done < len(dst) {
		if err := ctx.Err(); err != nil {
			return done, decryptor.readFailure(ctx, err)
		}
		absolute := offset + int64(done)
		chunkIndex := absolute / format.ChunkSize
		withinChunk := absolute % format.ChunkSize
		final := chunkIndex == finalIndex
		plaintextInChunk := int64(format.ChunkSize)
		if final {
			plaintextInChunk = decryptor.plaintextSize % format.ChunkSize
		}
		ciphertextSize := int(plaintextInChunk) + format.TagLen
		ciphertextOffset, ok := format.ChunkStoredOffset(chunkIndex)
		if !ok || ciphertextOffset+int64(ciphertextSize) > decryptor.storedSize {
			return done, ErrCiphertextSize
		}
		ciphertext := chunkBuffer[:ciphertextSize]
		if readErr := readContextAtFull(ctx, decryptor.source, ciphertext, ciphertextOffset); readErr != nil {
			return done, decryptor.readFailure(ctx, readErr)
		}
		nonce := format.BuildNonce(decryptor.noncePrefix, uint32(chunkIndex), final)
		plaintext, openErr := aead.Open(ciphertext[:0], nonce[:], ciphertext, nil)
		clear(nonce[:])
		if openErr != nil || int64(len(plaintext)) != plaintextInChunk {
			return done, stream.ErrAuthFailed
		}
		done += copy(dst[done:], plaintext[withinChunk:])
	}
	return done, nil
}

func (decryptor *RandomAccessDecryptor) readFailure(ctx context.Context, cause error) error {
	if decryptor.closed.Load() {
		return ErrDecryptorClosed
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) || errors.Is(cause, io.ErrNoProgress) {
		return stream.ErrAuthFailed
	}
	return cause
}

func (decryptor *RandomAccessDecryptor) Close() error {
	if decryptor == nil {
		return nil
	}
	decryptor.closeOnce.Do(func() {
		decryptor.closed.Store(true)
		if decryptor.cancelLifetime != nil {
			decryptor.cancelLifetime()
		}
		decryptor.mu.Lock()
		clear(decryptor.subkey)
		decryptor.subkey = nil
		decryptor.noncePrefix = [format.NoncePrefixLen]byte{}
		decryptor.source = nil
		decryptor.mu.Unlock()
	})
	return nil
}

func readContextAtFull(ctx context.Context, source ContextReaderAt, dst []byte, offset int64) error {
	read := 0
	for read < len(dst) {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := source.ReadAt(ctx, dst[read:], offset+int64(read))
		if n < 0 || n > len(dst)-read {
			return io.ErrUnexpectedEOF
		}
		read += n
		if read == len(dst) {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}
