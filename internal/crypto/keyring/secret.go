package keyring

import "golang.org/x/crypto/chacha20poly1305"

const accessSecretAADPrefix = "butterdb/access/v1|"

func secretAAD(accessKeyID string) []byte {
	return []byte(accessSecretAADPrefix + accessKeyID)
}

func (r *Ring) SealSecret(accessKeyID string, secret []byte) ([]byte, error) {
	if accessKeyID == "" {
		return nil, ErrEmptyAccessKeyID
	}
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRingClosed
	}
	aead, err := chacha20poly1305.NewX(r.secret.b[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(secret)+chacha20poly1305.Overhead)
	if err := r.readRandom(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out[:chacha20poly1305.NonceSizeX], secret, secretAAD(accessKeyID)), nil
}

func (r *Ring) OpenSecret(accessKeyID string, sealed []byte) ([]byte, error) {
	if accessKeyID == "" {
		return nil, ErrEmptyAccessKeyID
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRingClosed
	}
	if len(sealed) <= chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, ErrSecretUnseal
	}
	aead, err := chacha20poly1305.NewX(r.secret.b[:])
	if err != nil {
		return nil, ErrSecretUnseal
	}
	plain, err := aead.Open(nil, sealed[:chacha20poly1305.NonceSizeX], sealed[chacha20poly1305.NonceSizeX:], secretAAD(accessKeyID))
	if err != nil {
		return nil, ErrSecretUnseal
	}
	return plain, nil
}
