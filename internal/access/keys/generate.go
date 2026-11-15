package keys

import (
	"encoding/base64"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

const (
	idAlphabet        = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	idRandomLength    = access.AccessKeyIDLength - len(access.AccessKeyIDPrefix)
	idUnbiasedCeiling = 256 - 256%len(idAlphabet)
	secretRandomBytes = 30
	secretTextLength  = 40
)

func generateID(random io.Reader) (access.AccessKeyID, error) {
	chars := make([]byte, 0, idRandomLength)
	buf := make([]byte, idRandomLength)
	for len(chars) < idRandomLength {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", fmt.Errorf("keys: read random: %w", err)
		}
		for _, b := range buf {
			if int(b) >= idUnbiasedCeiling || len(chars) == idRandomLength {
				continue
			}
			chars = append(chars, idAlphabet[int(b)%len(idAlphabet)])
		}
	}
	return access.AccessKeyID(access.AccessKeyIDPrefix + string(chars)), nil
}

func generateSecret(random io.Reader) ([]byte, error) {
	raw := make([]byte, secretRandomBytes)
	defer clear(raw)
	if _, err := io.ReadFull(random, raw); err != nil {
		return nil, fmt.Errorf("keys: read random: %w", err)
	}
	text := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(text, raw)
	return text, nil
}
