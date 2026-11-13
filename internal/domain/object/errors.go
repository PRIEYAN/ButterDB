package object

import "errors"

var (
	ErrInvalidKey          = errors.New("invalid object key")
	ErrKeyEmpty            = errors.New("object key is empty")
	ErrKeyTooLong          = errors.New("object key is too long")
	ErrKeyNotUTF8          = errors.New("object key is not valid utf-8")
	ErrKeyControlCharacter = errors.New("object key contains a control character")
	ErrMetadataTooLarge    = errors.New("user metadata is too large")
	ErrInvalidMetadataName = errors.New("invalid user metadata name")
	ErrInvalidMetadataText = errors.New("invalid user metadata value")
	ErrInvalidETag         = errors.New("invalid etag")
	ErrNoParts             = errors.New("multipart etag needs at least one part")
	ErrTooManyParts        = errors.New("multipart etag has too many parts")
)
