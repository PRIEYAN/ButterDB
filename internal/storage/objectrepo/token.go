package objectrepo

import (
	"encoding/base64"
	"errors"
	"fmt"
)

var ErrInvalidContinuationToken = errors.New("objectrepo: invalid continuation token")

const (
	tokenVersion      byte = 0x01
	tokenAfterKey     byte = 0x00
	tokenAfterPrefix  byte = 0x01
	tokenHeaderLength      = 2
)

type resumePoint struct {
	value    string
	isPrefix bool
}

func (p resumePoint) token() string {
	kind := tokenAfterKey
	if p.isPrefix {
		kind = tokenAfterPrefix
	}
	raw := append([]byte{tokenVersion, kind}, p.value...)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseToken(token string) (resumePoint, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) <= tokenHeaderLength || raw[0] != tokenVersion {
		return resumePoint{}, ErrInvalidContinuationToken
	}
	switch raw[1] {
	case tokenAfterKey:
		return resumePoint{value: string(raw[tokenHeaderLength:])}, nil
	case tokenAfterPrefix:
		return resumePoint{value: string(raw[tokenHeaderLength:]), isPrefix: true}, nil
	}
	return resumePoint{}, fmt.Errorf("%w: unknown kind", ErrInvalidContinuationToken)
}

func (p resumePoint) lowerBound() (string, bool) {
	if p.isPrefix {
		return PrefixEnd(p.value)
	}
	return successor(p.value), true
}
