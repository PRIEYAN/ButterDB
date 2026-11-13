package keyring

import (
	"crypto/hkdf"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

const (
	fingerprintPrefix = "bkf1_"
	fingerprintBytes  = 16

	infoFingerprint = "butterdb/keyring/fingerprint/v1"
	infoWrap        = "butterdb/keyring/dek-wrap/v1"
	infoSecret      = "butterdb/keyring/access-key-secret/v1"
	infoSession     = "butterdb/keyring/session-seal/v1"
	infoSnapshot    = "butterdb/keyring/snapshot/v1"
	ownerIDLabel    = "butterdb/s3/owner-id/v1"
)

type Ring struct {
	fingerprint [fingerprintBytes]byte
	wrap        Key
	secret      Key
	session     Key
	snapshot    Key
	ownerID     string

	mu     sync.RWMutex
	closed bool
	randMu sync.Mutex
	rand   io.Reader
}

func Load(path string) (*Ring, error) {
	return LoadWithRand(path, cryptorand.Reader)
}

func LoadWithRand(path string, rand io.Reader) (*Ring, error) {
	if rand == nil {
		return nil, ErrNilRandom
	}
	root, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(root[:])
	ring, err := newRing(root[:], rand)
	if err != nil {
		return nil, err
	}
	return ring, nil
}

func newRing(root []byte, rand io.Reader) (_ *Ring, retErr error) {
	r := &Ring{rand: rand}
	defer func() {
		if retErr != nil {
			r.clearKeys()
		}
	}()
	fp, err := deriveBytes(root, infoFingerprint)
	if err != nil {
		return nil, err
	}
	copy(r.fingerprint[:], fp[:fingerprintBytes])
	clear(fp)
	for _, d := range []struct {
		dst  *Key
		info string
	}{
		{&r.wrap, infoWrap},
		{&r.secret, infoSecret},
		{&r.session, infoSession},
		{&r.snapshot, infoSnapshot},
	} {
		sub, err := deriveBytes(root, d.info)
		if err != nil {
			return nil, err
		}
		copy(d.dst.b[:], sub)
		clear(sub)
	}
	mac := hmac.New(sha256.New, root)
	mac.Write([]byte(ownerIDLabel))
	sum := mac.Sum(nil)
	r.ownerID = hex.EncodeToString(sum)
	clear(sum)
	return r, nil
}

func deriveBytes(root []byte, info string) ([]byte, error) {
	out, err := hkdf.Key(sha256.New, root, nil, info, keySize)
	if err != nil {
		return nil, fmt.Errorf("keyring: derive subkey: %w", err)
	}
	return out, nil
}

func (r *Ring) clearKeys() {
	r.wrap.Clear()
	r.secret.Clear()
	r.session.Clear()
	r.snapshot.Clear()
}

func (r *Ring) Fingerprint() [16]byte {
	return r.fingerprint
}

func (r *Ring) FingerprintString() string {
	return fingerprintPrefix + hex.EncodeToString(r.fingerprint[:])
}

func (r *Ring) OwnerID() string {
	return r.ownerID
}

func (r *Ring) SessionKey() *Key {
	return r.leaseKey(&r.session)
}

func (r *Ring) SnapshotKey() *Key {
	return r.leaseKey(&r.snapshot)
}

func (r *Ring) leaseKey(src *Key) *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil
	}
	return src.copyOf()
}

func (r *Ring) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.clearKeys()
}

func (r *Ring) readRandom(dst []byte) error {
	r.randMu.Lock()
	defer r.randMu.Unlock()
	if _, err := io.ReadFull(r.rand, dst); err != nil {
		return fmt.Errorf("keyring: read nonce: %w", err)
	}
	return nil
}

func (r *Ring) LogValue() slog.Value {
	return slog.StringValue(r.FingerprintString())
}

func (r *Ring) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, "keyring.Ring{"+r.FingerprintString()+"}")
}
