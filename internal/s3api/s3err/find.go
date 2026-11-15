package s3err

import "github.com/PRIEYAN/ButterDB/internal/access/sigv4"

func findSigV4(err error) (*sigv4.Error, bool) {
	if err == nil {
		return nil, false
	}
	if auth, ok := any(err).(*sigv4.Error); ok && auth != nil {
		return auth, true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return findSigV4(wrapped.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			if auth, ok := findSigV4(inner); ok {
				return auth, true
			}
		}
	}
	return nil, false
}
