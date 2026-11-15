package objectops

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const writerKeyPrefix = "key."

func validateKey(key string) error {
	err := object.Key(key).Validate()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, object.ErrKeyTooLong):
		e := s3err.New(s3err.CodeKeyTooLongError, "")
		e.Extra = []s3err.Field{
			{Name: "Size", Value: strconv.Itoa(len(key))},
			{Name: "MaxSizeAllowed", Value: strconv.Itoa(object.MaxKeyBytes)},
		}
		return e
	case errors.Is(err, object.ErrKeyNotUTF8):
		return invalidArgument("Object key is not valid UTF-8")
	case errors.Is(err, object.ErrKeyControlCharacter):
		return invalidArgument("Object key contains a control character")
	}
	return invalidArgument("Object key is not valid")
}

func entityTooLarge(proposed int64) error {
	e := s3err.New(s3err.CodeEntityTooLarge, "")
	e.Extra = []s3err.Field{
		{Name: "ProposedSize", Value: strconv.FormatInt(proposed, 10)},
		{Name: "MaxSizeAllowed", Value: strconv.FormatInt(limits.MaxStoredObjectBytes, 10)},
	}
	return e
}

func checkStoredSize(plaintextSize int64, encrypted bool) error {
	if !encrypted {
		if plaintextSize > limits.MaxStoredObjectBytes {
			return entityTooLarge(plaintextSize)
		}
		return nil
	}
	if format.ValidatePlaintextSize(plaintextSize) != nil || plaintextSize > limits.MaxEncryptedPlaintextBytes {
		return entityTooLarge(plaintextSize)
	}
	if format.CiphertextSize(plaintextSize) > limits.MaxStoredObjectBytes {
		return entityTooLarge(plaintextSize)
	}
	return nil
}

func checkPutRecord(key, contentType string, user, system map[string]string, accessKeyID string) error {
	writer := writerKeyPrefix + accessKeyID
	length, err := wire.PutRecordLen(key, contentType, user, system, writer)
	if err != nil {
		return invalidArgument("The object key or metadata cannot be recorded")
	}
	if length <= limits.MaxPutOpChars {
		return nil
	}
	bare, err := wire.PutRecordLen(key, contentType, nil, nil, writer)
	if err == nil && bare > limits.MaxPutOpChars {
		return keyTooLongForRecord(len(key))
	}
	e := s3err.New(s3err.CodeMetadataTooLarge, fmt.Sprintf("The encoded object record is %d characters; the limit is %d", length, limits.MaxPutOpChars))
	e.Extra = []s3err.Field{
		{Name: "Size", Value: strconv.Itoa(length)},
		{Name: "MaxSizeAllowed", Value: strconv.Itoa(limits.MaxPutOpChars)},
	}
	return e
}

func keyTooLongForRecord(size int) error {
	e := s3err.New(s3err.CodeKeyTooLongError, "")
	e.Extra = []s3err.Field{
		{Name: "Size", Value: strconv.Itoa(size)},
		{Name: "MaxSizeAllowed", Value: strconv.Itoa(object.MaxKeyBytes)},
	}
	return e
}

func parseContentMD5(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	sum, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(sum) != md5.Size {
		return nil, s3err.New(s3err.CodeInvalidDigest, "")
	}
	return sum, nil
}

type md5VerifyingBody struct {
	source io.Reader
	want   []byte
	hash   hash.Hash
	done   bool
}

func newMD5VerifyingBody(source io.Reader, want []byte) io.Reader {
	if want == nil {
		return source
	}
	return &md5VerifyingBody{source: source, want: want, hash: md5.New()}
}

func (b *md5VerifyingBody) Read(p []byte) (int, error) {
	n, err := b.source.Read(p)
	if n > 0 {
		b.hash.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && !b.done {
		b.done = true
		if subtle.ConstantTimeCompare(b.hash.Sum(nil), b.want) != 1 {
			return n, fmt.Errorf("objectops: content-md5: %w", errs.ErrBadDigest)
		}
	}
	return n, err
}
