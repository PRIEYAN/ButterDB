package handlers

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"hash"
	"io"
)

type digestVerifyingBody struct {
	source     io.Reader
	md5Want    []byte
	sha256Want []byte
	md5Hash    hash.Hash
	sha256Hash hash.Hash
	checked    bool
}

func newDigestVerifyingBody(source io.Reader, md5Want, sha256Want []byte) io.Reader {
	if md5Want == nil && sha256Want == nil {
		return source
	}
	body := &digestVerifyingBody{source: source, md5Want: md5Want, sha256Want: sha256Want}
	if md5Want != nil {
		body.md5Hash = md5.New()
	}
	if sha256Want != nil {
		body.sha256Hash = sha256.New()
	}
	return body
}

func (b *digestVerifyingBody) Read(p []byte) (int, error) {
	n, err := b.source.Read(p)
	if n > 0 {
		b.absorb(p[:n])
	}
	if err == io.EOF && !b.checked {
		b.checked = true
		if mismatch := b.mismatch(); mismatch != nil {
			return n, mismatch
		}
	}
	return n, err
}

func (b *digestVerifyingBody) absorb(chunk []byte) {
	if b.md5Hash != nil {
		b.md5Hash.Write(chunk)
	}
	if b.sha256Hash != nil {
		b.sha256Hash.Write(chunk)
	}
}

func (b *digestVerifyingBody) mismatch() error {
	if b.md5Hash != nil && subtle.ConstantTimeCompare(b.md5Hash.Sum(nil), b.md5Want) != 1 {
		return &DigestMismatchError{Algorithm: "md5"}
	}
	if b.sha256Hash != nil && subtle.ConstantTimeCompare(b.sha256Hash.Sum(nil), b.sha256Want) != 1 {
		return &DigestMismatchError{Algorithm: "sha256"}
	}
	return nil
}
