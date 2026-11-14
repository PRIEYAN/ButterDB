package keyring

import (
	"crypto/subtle"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	wrappedDEKSize = chacha20poly1305.NonceSizeX + keySize + chacha20poly1305.Overhead
	keyCheckSize   = chacha20poly1305.NonceSizeX + len(keyCheckPlaintext) + chacha20poly1305.Overhead

	dekAADPrefix      = "butterdb/dek/v1|"
	keyCheckPlaintext = "butterDB-key-check-v1"
)

func dekAAD(bucketChannelID int64, kid string) []byte {
	return []byte(dekAADPrefix + strconv.FormatInt(bucketChannelID, 10) + "|" + kid)
}

func (r *Ring) WrapDEK(dek *Key, bucketChannelID int64, kid string) (wrap [72]byte, check [61]byte, err error) {
	defer func() {
		if err != nil {
			wrap = [wrappedDEKSize]byte{}
			check = [keyCheckSize]byte{}
		}
	}()
	if dek == nil {
		return wrap, check, ErrNilKey
	}
	if !validKeyID(kid) {
		return wrap, check, ErrInvalidKeyID
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return wrap, check, ErrRingClosed
	}
	wrapAEAD, err := chacha20poly1305.NewX(r.wrap.b[:])
	if err != nil {
		return wrap, check, err
	}
	if err := r.readRandom(wrap[:chacha20poly1305.NonceSizeX]); err != nil {
		return wrap, check, err
	}
	wrapAEAD.Seal(wrap[chacha20poly1305.NonceSizeX:chacha20poly1305.NonceSizeX], wrap[:chacha20poly1305.NonceSizeX], dek.b[:], dekAAD(bucketChannelID, kid))

	checkAEAD, err := chacha20poly1305.NewX(dek.b[:])
	if err != nil {
		return wrap, check, err
	}
	if err := r.readRandom(check[:chacha20poly1305.NonceSizeX]); err != nil {
		return wrap, check, err
	}
	checkAEAD.Seal(check[chacha20poly1305.NonceSizeX:chacha20poly1305.NonceSizeX], check[:chacha20poly1305.NonceSizeX], []byte(keyCheckPlaintext), nil)
	return wrap, check, nil
}

func (r *Ring) UnwrapDEK(wrap []byte, check []byte, bucketChannelID int64, kid string) (*Key, error) {
	dek := &Key{}
	if err := r.unwrapInto(dek, wrap, check, bucketChannelID, kid); err != nil {
		return nil, err
	}
	return dek, nil
}

func (r *Ring) unwrapInto(dek *Key, wrap, check []byte, bucketChannelID int64, kid string) (retErr error) {
	defer func() {
		if retErr != nil {
			dek.Clear()
		}
	}()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRingClosed
	}
	if !validKeyID(kid) || len(wrap) != wrappedDEKSize {
		return ErrDEKUnwrap
	}
	wrapAEAD, err := chacha20poly1305.NewX(r.wrap.b[:])
	if err != nil {
		return ErrDEKUnwrap
	}
	var plain [keySize]byte
	defer clear(plain[:])
	opened, err := wrapAEAD.Open(plain[:0], wrap[:chacha20poly1305.NonceSizeX], wrap[chacha20poly1305.NonceSizeX:], dekAAD(bucketChannelID, kid))
	if err != nil || len(opened) != keySize {
		return ErrDEKUnwrap
	}
	copy(dek.b[:], plain[:])

	if len(check) != keyCheckSize {
		return ErrDEKCheck
	}
	checkAEAD, err := chacha20poly1305.NewX(dek.b[:])
	if err != nil {
		return ErrDEKCheck
	}
	var marker [len(keyCheckPlaintext)]byte
	got, err := checkAEAD.Open(marker[:0], check[:chacha20poly1305.NonceSizeX], check[chacha20poly1305.NonceSizeX:], nil)
	if err != nil || subtle.ConstantTimeCompare(got, []byte(keyCheckPlaintext)) != 1 {
		return ErrDEKCheck
	}
	return nil
}
