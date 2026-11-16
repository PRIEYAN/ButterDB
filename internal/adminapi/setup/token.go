package setup

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

const (
	setupTokenPrefix      = "bdst1_"
	setupTokenRandomBytes = 20
	setupTokenBodyLength  = 32
	secretHashDomain      = "butterdb-admin-v1\x00"
	secretKindSetup       = "setup"
)

var setupTokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func hashSetupToken(token string) [32]byte {
	h := sha256.New()
	h.Write([]byte(secretHashDomain))
	h.Write([]byte(secretKindSetup))
	h.Write([]byte{0})
	h.Write([]byte(token))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func newSetupToken(random io.Reader) (string, error) {
	raw := make([]byte, setupTokenRandomBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", fmt.Errorf("setup: read randomness: %w", err)
	}
	return setupTokenPrefix + strings.ToLower(setupTokenEncoding.EncodeToString(raw)), nil
}

func wellFormedSetupToken(token string) bool {
	body, found := strings.CutPrefix(token, setupTokenPrefix)
	if !found || len(body) != setupTokenBodyLength {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

func (s *Service) MintSetupTokenIfUnclaimed(ctx context.Context) (string, time.Time, error) {
	state, err := s.claims.ClaimState(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("setup: read claim state: %w", err)
	}
	if state != ClaimStateUnclaimed {
		return "", time.Time{}, nil
	}
	token, err := newSetupToken(s.random)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.clock.Now().Add(s.ttl).UTC().Truncate(time.Millisecond)
	if err := s.claims.ReplaceSetupToken(ctx, hashSetupToken(token), expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("setup: store setup token: %w", err)
	}
	s.log.Warn("setup token", slog.String("setup_token", token), slog.Time("expires_at", expiresAt))
	return token, expiresAt, nil
}
