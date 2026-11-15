package sigv4

import (
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func (e *Error) Error() string {
	return "sigv4: " + reasonText(e.Reason)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func newError(reason Reason) *Error {
	return &Error{Reason: reason, Err: sentinelFor(reason)}
}

func sentinelFor(reason Reason) error {
	switch reason {
	case ReasonSignature, ReasonHeaderMalformed, ReasonQueryMalformed, ReasonWrongRegion:
		return errs.ErrSignatureMismatch
	case ReasonUnknownKey:
		return errs.ErrUnknownAccessKey
	case ReasonSkew, ReasonExpired, ReasonNotYetValid:
		return errs.ErrRequestExpired
	case ReasonMissingDate:
		return errs.ErrAccessDenied
	case ReasonContentSHA256Mismatch, ReasonChecksumMismatch:
		return errs.ErrBadDigest
	case ReasonSigV4A:
		return errs.ErrNotImplemented
	default:
		return nil
	}
}

func reasonText(reason Reason) string {
	switch reason {
	case ReasonSignature:
		return "signature does not match"
	case ReasonUnknownKey:
		return "unknown access key"
	case ReasonHeaderMalformed:
		return "authorization header malformed"
	case ReasonQueryMalformed:
		return "authorization query parameters malformed"
	case ReasonWrongRegion:
		return "credential scope region not served"
	case ReasonSkew:
		return "request time too skewed"
	case ReasonExpired:
		return "presigned request expired"
	case ReasonNotYetValid:
		return "presigned request not yet valid"
	case ReasonMissingDate:
		return "missing date header"
	case ReasonMissingContentSHA256:
		return "missing x-amz-content-sha256"
	case ReasonBadContentSHA256:
		return "invalid x-amz-content-sha256"
	case ReasonContentSHA256Mismatch:
		return "payload sha256 does not match x-amz-content-sha256"
	case ReasonSecurityToken:
		return "security tokens are not supported"
	case ReasonSigV2:
		return "signature version 2 is not supported"
	case ReasonSigV4A:
		return "signature version 4a is not implemented"
	case ReasonBothMechanisms:
		return "more than one authentication mechanism"
	case ReasonUnsupportedScheme:
		return "unsupported authorization scheme"
	case ReasonChunkFraming:
		return "invalid aws-chunked framing"
	case ReasonIncompleteBody:
		return "incomplete request body"
	case ReasonChecksumMismatch:
		return "payload checksum mismatch"
	case ReasonChecksumMalformed:
		return "malformed payload checksum"
	default:
		return "authentication failed"
	}
}

func fmtPresignError(problem string) error {
	return fmt.Errorf("sigv4: presign: %s", problem)
}
