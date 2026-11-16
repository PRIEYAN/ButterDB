package session

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrUnauthenticated          = errors.New("session: unauthenticated")
	ErrDeviceRevoked            = errors.New("session: device revoked")
	ErrRateLimited              = errors.New("session: rate limited")
	ErrNotClaimed               = errors.New("session: server not claimed")
	ErrPairingCodeInvalid       = errors.New("session: pairing code invalid")
	ErrPairingUnavailable       = errors.New("session: pairing delivery unavailable")
	ErrUnsupportedDelivery      = errors.New("session: unsupported pairing delivery")
	ErrMalformedToken           = errors.New("session: malformed device token")
	ErrControlSocketUnreachable = errors.New("session: control socket unreachable")
	ErrControlProtocol          = errors.New("session: control socket protocol error")
)

type RateLimitedError struct {
	Scope      Scope
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("session: rate limited: scope %s", e.Scope)
}

func (e *RateLimitedError) Is(target error) bool {
	return target == ErrRateLimited
}
