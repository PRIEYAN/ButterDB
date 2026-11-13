package bucket

import "errors"

var (
	ErrInvalidBucketName  = errors.New("invalid bucket name")
	ErrInvalidPolicy      = errors.New("invalid bucket policy")
	ErrInvalidEncryption  = errors.New("invalid bucket default encryption")
	ErrInvalidCacheMaxAge = errors.New("invalid bucket cache max age")
)
