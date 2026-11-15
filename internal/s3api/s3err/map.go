package s3err

import (
	"errors"
	"strconv"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	defaultSlowDownRetry  = 5 * time.Second
	uncertainOutcomeRetry = 30 * time.Second
	maxAllowedSkewMillis  = "900000"
)

func Map(err error) APIError {
	if err == nil {
		return New(CodeInternalError, "")
	}
	var typed APIError
	if errors.As(err, &typed) {
		return typed
	}
	if mapped, ok := mapTyped(err); ok {
		return mapped
	}
	if mapped, ok := mapLocalSentinel(err); ok {
		return mapped
	}
	if mapped, ok := mapDomain(err); ok {
		return mapped
	}
	return New(CodeInternalError, "")
}

func mapTyped(err error) (APIError, bool) {
	if auth, ok := findSigV4(err); ok {
		return MapSigV4(auth), true
	}
	var slow *admission.SlowDownError
	if errors.As(err, &slow) {
		e := New(CodeSlowDown, "")
		e.RetryAfter = max(slow.RetryAfter, time.Second)
		return e, true
	}
	var unsatisfiable *httprange.UnsatisfiableRangeError
	if errors.As(err, &unsatisfiable) {
		e := New(CodeInvalidRange, "")
		e.Extra = []Field{
			{Name: "RangeRequested", Value: unsatisfiable.Requested},
			{Name: "ActualObjectSize", Value: strconv.FormatInt(unsatisfiable.Size, 10)},
		}
		return e, true
	}
	var precondition *httprange.PreconditionError
	if errors.As(err, &precondition) {
		e := New(CodePreconditionFailed, "")
		e.Extra = []Field{{Name: "Condition", Value: precondition.Condition}}
		return e, true
	}
	return APIError{}, false
}

func mapLocalSentinel(err error) (APIError, bool) {
	switch {
	case errors.Is(err, put.ErrCommitOutcomeUnknown):
		e := New(CodeSlowDown, "")
		e.RetryAfter = uncertainOutcomeRetry
		return e, true
	case errors.Is(err, put.ErrIdempotencyConflict):
		return New(CodeOperationAborted, ""), true
	case errors.Is(err, spool.ErrLengthMismatch):
		return New(CodeIncompleteBody, ""), true
	case errors.Is(err, xmlcodec.ErrBodyTooLarge):
		return New(CodeMaxMessageLengthExceeded, ""), true
	case errors.Is(err, xmlcodec.ErrEmptyBody):
		return New(CodeMissingRequestBodyError, ""), true
	case errors.Is(err, xmlcodec.ErrMalformed), errors.Is(err, xmlcodec.ErrDoctype):
		return New(CodeMalformedXML, ""), true
	case errors.Is(err, bucket.ErrInvalidBucketName):
		return New(CodeInvalidBucketName, ""), true
	case errors.Is(err, object.ErrKeyTooLong):
		return New(CodeKeyTooLongError, ""), true
	case errors.Is(err, object.ErrMetadataTooLarge):
		return New(CodeMetadataTooLarge, ""), true
	}
	return APIError{}, false
}

func mapDomain(err error) (APIError, bool) {
	for _, entry := range domainTable() {
		if errors.Is(err, entry.sentinel) {
			return entry.build(), true
		}
	}
	if isInvalidArgument(err) {
		return New(CodeInvalidArgument, ""), true
	}
	return APIError{}, false
}

func isInvalidArgument(err error) bool {
	return errors.Is(err, put.ErrInvalidRequest) ||
		errors.Is(err, object.ErrInvalidKey) ||
		errors.Is(err, object.ErrInvalidMetadataName) ||
		errors.Is(err, object.ErrInvalidMetadataText)
}

type domainEntry struct {
	sentinel error
	build    func() APIError
}

func plain(code Code) func() APIError {
	return func() APIError { return New(code, "") }
}

func domainTable() []domainEntry {
	return []domainEntry{
		{errs.ErrNoSuchBucket, plain(CodeNoSuchBucket)},
		{errs.ErrNoSuchKey, plain(CodeNoSuchKey)},
		{errs.ErrBucketExists, plain(CodeBucketAlreadyOwnedByYou)},
		{errs.ErrBucketNotEmpty, plain(CodeBucketNotEmpty)},
		{errs.ErrInvalidBucketName, plain(CodeInvalidBucketName)},
		{errs.ErrInvalidKey, plain(CodeInvalidArgument)},
		{errs.ErrObjectTooLarge, entityTooLarge},
		{errs.ErrMetadataTooLarge, plain(CodeMetadataTooLarge)},
		{errs.ErrEmptyFile, plain(CodeInvalidArgument)},
		{errs.ErrAccessDenied, plain(CodeAccessDenied)},
		{errs.ErrUnknownAccessKey, plain(CodeInvalidAccessKeyId)},
		{errs.ErrAccessKeyNotFound, plain(CodeInvalidAccessKeyId)},
		{errs.ErrAccessKeyRevoked, plain(CodeInvalidAccessKeyId)},
		{errs.ErrSignatureMismatch, plain(CodeSignatureDoesNotMatch)},
		{errs.ErrRequestExpired, requestExpired},
		{errs.ErrPreconditionFailed, plain(CodePreconditionFailed)},
		{errs.ErrInvalidRange, plain(CodeInvalidRange)},
		{errs.ErrBadDigest, plain(CodeBadDigest)},
		{errs.ErrNotImplemented, plain(CodeNotImplemented)},
		{errs.ErrNoSuchUpload, plain(CodeNoSuchUpload)},
		{errs.ErrInvalidPart, plain(CodeInvalidPart)},
		{errs.ErrInvalidPartOrder, plain(CodeInvalidPartOrder)},
		{errs.ErrEntityTooSmall, entityTooSmall},
		{errs.ErrUploadBusy, plain(CodeOperationAborted)},
		{errs.ErrSlowDown, slowDown},
		{errs.ErrServiceUnavailable, serviceUnavailable},
		{errs.ErrKeyUnavailable, plain(CodeInternalError)},
		{errs.ErrMissingDependency, plain(CodeInternalError)},
	}
}

func entityTooLarge() APIError {
	e := New(CodeEntityTooLarge, "")
	e.Extra = []Field{{Name: "MaxSizeAllowed", Value: strconv.FormatInt(limits.MaxStoredObjectBytes, 10)}}
	return e
}

func entityTooSmall() APIError {
	e := New(CodeEntityTooSmall, "")
	e.Extra = []Field{{Name: "MinSizeAllowed", Value: strconv.FormatInt(limits.MinMultipartPartBytes, 10)}}
	return e
}

func requestExpired() APIError {
	return New(CodeAccessDenied, "Request has expired")
}

func slowDown() APIError {
	e := New(CodeSlowDown, "")
	e.RetryAfter = defaultSlowDownRetry
	return e
}

func serviceUnavailable() APIError {
	e := New(CodeServiceUnavailable, "")
	e.RetryAfter = uncertainOutcomeRetry
	return e
}
