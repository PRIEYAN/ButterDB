package access

import "errors"

var (
	ErrInvalidAccessKeyID = errors.New("invalid access key id")
	ErrInvalidAction      = errors.New("invalid access action")
	ErrInvalidScope       = errors.New("invalid access scope")
)
