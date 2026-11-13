package format

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	Magic          = "TDE1"
	HeaderLen      = 4 + 1 + 1 + 16 + 20 + 8
	ChunkSizeLog2  = 16
	ChunkSize      = 1 << ChunkSizeLog2
	SaltLen        = 16
	NoncePrefixLen = 20
	NonceLen       = 24
	TagLen         = 16
	FinalChunkBit  = uint32(1) << 31

	MaxPlaintextSize = int64(FinalChunkBit)*int64(ChunkSize) - 1

	maxStoredObjectBytes = 1900 * 1024 * 1024
	chunkStride          = ChunkSize + TagLen
	minStoredSize        = HeaderLen + TagLen
	fitFullChunks        = (maxStoredObjectBytes - minStoredSize) / chunkStride
	fitTailBytes         = maxStoredObjectBytes - minStoredSize - fitFullChunks*chunkStride

	MaxEncryptedPlaintextBytes = fitFullChunks*ChunkSize + min(ChunkSize-1, fitTailBytes)
)

var (
	ErrShortHeader           = errors.New("format: encrypted stream truncated header")
	ErrBadMagic              = errors.New("format: not a TDE1 encrypted stream")
	ErrUnsupported           = errors.New("format: unsupported stream version")
	ErrNegativePlaintextSize = errors.New("format: negative plaintext size")
	ErrPlaintextTooLarge     = errors.New("format: plaintext size exceeds TDE1 stream capacity")
)

type Header struct {
	Salt          [SaltLen]byte
	NoncePrefix   [NoncePrefixLen]byte
	PlaintextSize int64
}

func ValidatePlaintextSize(plaintextSize int64) error {
	if plaintextSize < 0 {
		return ErrNegativePlaintextSize
	}
	if plaintextSize > MaxPlaintextSize {
		return ErrPlaintextTooLarge
	}
	return nil
}

func EncodeHeader(h Header) ([HeaderLen]byte, error) {
	var out [HeaderLen]byte
	if err := ValidatePlaintextSize(h.PlaintextSize); err != nil {
		return out, err
	}
	copy(out[0:4], Magic)
	out[4] = 0
	out[5] = ChunkSizeLog2
	copy(out[6:6+SaltLen], h.Salt[:])
	copy(out[22:22+NoncePrefixLen], h.NoncePrefix[:])
	binary.LittleEndian.PutUint64(out[42:50], uint64(h.PlaintextSize))
	return out, nil
}

func DecodeHeader(b []byte) (Header, error) {
	var h Header
	if len(b) < HeaderLen {
		return h, ErrShortHeader
	}
	if string(b[0:4]) != Magic {
		return h, ErrBadMagic
	}
	if b[4] != 0 || b[5] != ChunkSizeLog2 {
		return h, ErrUnsupported
	}
	size := binary.LittleEndian.Uint64(b[42:50])
	if size > uint64(MaxPlaintextSize) {
		return h, ErrPlaintextTooLarge
	}
	copy(h.Salt[:], b[6:6+SaltLen])
	copy(h.NoncePrefix[:], b[22:22+NoncePrefixLen])
	h.PlaintextSize = int64(size)
	return h, nil
}

func BuildNonce(prefix [NoncePrefixLen]byte, counter uint32, final bool) [NonceLen]byte {
	var nonce [NonceLen]byte
	copy(nonce[:], prefix[:])
	if final {
		counter |= FinalChunkBit
	}
	binary.BigEndian.PutUint32(nonce[NoncePrefixLen:], counter)
	return nonce
}

func CiphertextSize(plaintextSize int64) int64 {
	if plaintextSize < 0 {
		return 0
	}
	if plaintextSize > MaxPlaintextSize {
		return math.MaxInt64
	}
	chunks := plaintextSize/ChunkSize + 1
	base := int64(HeaderLen) + plaintextSize
	if base < plaintextSize {
		return math.MaxInt64
	}
	maxTags := (math.MaxInt64 - base) / int64(TagLen)
	if chunks > maxTags {
		return math.MaxInt64
	}
	return base + chunks*int64(TagLen)
}

func ChunkStoredOffset(chunkIndex int64) (int64, bool) {
	if chunkIndex < 0 {
		return 0, false
	}
	if chunkIndex > (math.MaxInt64-int64(HeaderLen))/int64(chunkStride) {
		return 0, false
	}
	return int64(HeaderLen) + chunkIndex*int64(chunkStride), true
}

func StoredOffset(plaintextOffset int64) (int64, bool) {
	if plaintextOffset < 0 {
		return 0, false
	}
	base, ok := ChunkStoredOffset(plaintextOffset / ChunkSize)
	if !ok {
		return 0, false
	}
	return base + plaintextOffset%ChunkSize, true
}
