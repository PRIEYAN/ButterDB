package keyring

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
)

const (
	keySize     = 32
	keyIDBytes  = 8
	redactedKey = "[REDACTED]"
)

type Key struct {
	b [keySize]byte
}

func NewKey(raw []byte) (*Key, error) {
	if len(raw) != keySize {
		return nil, ErrKeyLength
	}
	k := &Key{}
	copy(k.b[:], raw)
	return k, nil
}

func GenerateKey(rand io.Reader) (*Key, error) {
	if rand == nil {
		return nil, ErrNilRandom
	}
	k := &Key{}
	if _, err := io.ReadFull(rand, k.b[:]); err != nil {
		k.Clear()
		return nil, fmt.Errorf("keyring: read key bytes: %w", err)
	}
	return k, nil
}

func NewKeyID(rand io.Reader) (string, error) {
	if rand == nil {
		return "", ErrNilRandom
	}
	var raw [keyIDBytes]byte
	if _, err := io.ReadFull(rand, raw[:]); err != nil {
		return "", fmt.Errorf("keyring: read key id bytes: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (k *Key) Bytes() []byte {
	if k == nil {
		return nil
	}
	return k.b[:]
}

func (k *Key) Clear() {
	if k == nil {
		return
	}
	clear(k.b[:])
}

func (k Key) LogValue() slog.Value {
	return slog.StringValue(redactedKey)
}

func (k Key) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, redactedKey)
}

func (k *Key) copyOf() *Key {
	if k == nil {
		return nil
	}
	c := &Key{}
	c.b = k.b
	return c
}

func validKeyID(kid string) bool {
	if len(kid) != keyIDBytes*2 {
		return false
	}
	for i := 0; i < len(kid); i++ {
		c := kid[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
