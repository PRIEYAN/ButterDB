package session

import (
	"encoding/base32"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	deviceTokenPrefix = "bdt1_"
	deviceIDRawBytes  = 10
	deviceIDLength    = 16
	secretRawBytes    = 32
	secretLength      = 52
)

var lowerBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type Tokens struct {
	mu     sync.Mutex
	random io.Reader
}

func NewTokens(random io.Reader) *Tokens {
	return &Tokens{random: random}
}

func (t *Tokens) NewDeviceToken() (token string, deviceID string, hash [32]byte, err error) {
	if t == nil || t.random == nil {
		return "", "", [32]byte{}, fmt.Errorf("session: %w: random", errs.ErrMissingDependency)
	}
	var raw [deviceIDRawBytes + secretRawBytes]byte
	t.mu.Lock()
	_, err = io.ReadFull(t.random, raw[:])
	t.mu.Unlock()
	if err != nil {
		return "", "", [32]byte{}, fmt.Errorf("session: read randomness: %w", err)
	}
	deviceID = encodeLower(raw[:deviceIDRawBytes])
	secret := encodeLower(raw[deviceIDRawBytes:])
	token = deviceTokenPrefix + deviceID + "_" + secret
	return token, deviceID, HashSecret(KindDevice, token), nil
}

func encodeLower(raw []byte) string {
	return strings.ToLower(lowerBase32.EncodeToString(raw))
}

func ParseDeviceToken(token string) (deviceID string, err error) {
	rest, ok := strings.CutPrefix(token, deviceTokenPrefix)
	if !ok || len(rest) != deviceIDLength+1+secretLength || rest[deviceIDLength] != '_' {
		return "", ErrMalformedToken
	}
	id, secret := rest[:deviceIDLength], rest[deviceIDLength+1:]
	if !isLowerBase32(id) || !isLowerBase32(secret) {
		return "", ErrMalformedToken
	}
	return id, nil
}

func isLowerBase32(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
