package tlscert

import (
	"encoding/hex"
	"strings"
)

const (
	fingerprintPrefix  = "SHA256:"
	displayGroupLength = 4
)

func Fingerprint(spki [32]byte) string {
	return fingerprintPrefix + hex.EncodeToString(spki[:])
}

func DisplayFingerprint(spki [32]byte) string {
	digits := hex.EncodeToString(spki[:])
	groups := make([]string, 0, len(digits)/displayGroupLength)
	for start := 0; start < len(digits); start += displayGroupLength {
		groups = append(groups, digits[start:start+displayGroupLength])
	}
	return strings.Join(groups, " ")
}
