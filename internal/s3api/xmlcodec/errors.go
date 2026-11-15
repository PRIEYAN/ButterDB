package xmlcodec

import "errors"

var (
	ErrBodyTooLarge = errors.New("xmlcodec: body exceeds its limit")
	ErrEmptyBody    = errors.New("xmlcodec: body is empty")
	ErrMalformed    = errors.New("xmlcodec: body is not well-formed xml")
	ErrDoctype      = errors.New("xmlcodec: doctype declarations are not accepted")
)
