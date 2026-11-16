package setup

import (
	"errors"
	"fmt"
)

var (
	ErrAlreadyClaimed    = errors.New("setup: server already claimed")
	ErrSetupTokenInvalid = errors.New("setup: setup token invalid")
	ErrValidationFailed  = errors.New("setup: validation failed")
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("setup: validation failed: %s: %s", e.Field, e.Reason)
}

func (e *ValidationError) Is(target error) bool {
	return target == ErrValidationFailed
}
