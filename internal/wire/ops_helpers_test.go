package wire

import "errors"

func isMalformed(err error) bool {
	return errors.Is(err, ErrWireMalformed)
}
