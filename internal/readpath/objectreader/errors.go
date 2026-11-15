package objectreader

import "errors"

var (
	ErrEncryptedUnsupported = errors.New("objectreader: encrypted object format is unsupported")
	ErrClosed               = errors.New("objectreader: opener is closed")
	ErrReaderClosed         = errors.New("objectreader: reader is closed")
	ErrNilContext           = errors.New("objectreader: context is required")
	ErrInvalidMeta          = errors.New("objectreader: invalid object metadata")
	ErrDocumentMismatch     = errors.New("objectreader: telegram document does not match the projected object")
	ErrNegativeOffset       = errors.New("objectreader: negative read offset")
	errCoalescerClosed      = errors.New("objectreader: coalescer is closed")
)
