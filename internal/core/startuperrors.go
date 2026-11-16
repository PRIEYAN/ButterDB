package core

import (
	"errors"
	"fmt"
)

type StartupReason string

const (
	ReasonStateInvalid           StartupReason = "state_invalid"
	ReasonRegistryUnknown        StartupReason = "registry_unknown"
	ReasonKEKMismatch            StartupReason = "kek_mismatch"
	ReasonRegistryMarkerMismatch StartupReason = "registry_marker_mismatch"
	ReasonFormatTooNew           StartupReason = "format_too_new"
	ReasonProjectionMismatch     StartupReason = "projection_mismatch"
	ReasonForeignRegistry        StartupReason = "registry_foreign"
	ReasonKeyMissing             StartupReason = "server_key_missing"
)

var (
	ErrKeyOrRegistry       = errors.New("core: key or registry check failed")
	ErrIncompatibleData    = errors.New("core: data directory is incompatible")
	ErrRegistryNotAdopted  = errors.New("core: no registry was adopted")
	ErrNoPrimaryAccount    = errors.New("core: no primary telegram account is linked")
	ErrRegistryChoiceUnset = errors.New("core: registry choice was not confirmed")
)

type StartupError struct {
	Reason       StartupReason
	Detail       string
	Incompatible bool
}

func (e *StartupError) Error() string {
	return fmt.Sprintf("core: startup check %s: %s", e.Reason, e.Detail)
}

func (e *StartupError) Is(target error) bool {
	if e.Incompatible {
		return target == ErrIncompatibleData
	}
	return target == ErrKeyOrRegistry
}

func keyFailure(reason StartupReason, detail string) error {
	return &StartupError{Reason: reason, Detail: detail}
}

func incompatibleFailure(reason StartupReason, detail string) error {
	return &StartupError{Reason: reason, Detail: detail, Incompatible: true}
}
