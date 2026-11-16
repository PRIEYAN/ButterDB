package session

import "crypto/sha256"

type SecretKind string

const (
	KindSetup   SecretKind = "setup"
	KindDevice  SecretKind = "device"
	KindPairing SecretKind = "pairing"
)

const hashDomain = "butterdb-admin-v1\x00"

func HashSecret(kind SecretKind, normalised string) [32]byte {
	h := sha256.New()
	h.Write([]byte(hashDomain))
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(normalised))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
