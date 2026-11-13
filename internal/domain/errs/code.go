package errs

import "errors"

const (
	CodeNoSuchBucket       = "bucket.not_found"
	CodeNoSuchKey          = "object.not_found"
	CodeBucketExists       = "bucket.exists"
	CodeBucketNotEmpty     = "bucket.not_empty"
	CodeInvalidBucketName  = "bucket.invalid_name"
	CodeInvalidKey         = "object.invalid_key"
	CodeObjectTooLarge     = "object.too_large"
	CodeMetadataTooLarge   = "object.metadata_too_large"
	CodeEmptyFile          = "object.empty"
	CodeAccessDenied       = "access.denied"
	CodeUnknownAccessKey   = "access.unknown_key"
	CodeAccessKeyNotFound  = "access.key_not_found"
	CodeAccessKeyRevoked   = "access.key_revoked"
	CodeSignatureMismatch  = "access.signature_mismatch"
	CodeRequestExpired     = "access.request_expired"
	CodePreconditionFailed = "request.precondition_failed"
	CodeInvalidRange       = "request.invalid_range"
	CodeBadDigest          = "request.bad_digest"
	CodeNotImplemented     = "request.not_implemented"
	CodeNoSuchUpload       = "multipart.no_such_upload"
	CodeInvalidPart        = "multipart.invalid_part"
	CodeInvalidPartOrder   = "multipart.invalid_part_order"
	CodeEntityTooSmall     = "multipart.entity_too_small"
	CodeUploadBusy         = "multipart.upload_busy"
	CodeSlowDown           = "telegram.slow_down"
	CodeServiceUnavailable = "server.unavailable"
	CodeKeyUnavailable     = "encryption.key_unavailable"
	CodeMissingDependency  = "internal.missing_dependency"
	CodeInternal           = "internal.error"
)

type mapping struct {
	sentinel error
	code     string
}

func catalogue() []mapping {
	return []mapping{
		{ErrNoSuchBucket, CodeNoSuchBucket},
		{ErrNoSuchKey, CodeNoSuchKey},
		{ErrBucketExists, CodeBucketExists},
		{ErrBucketNotEmpty, CodeBucketNotEmpty},
		{ErrInvalidBucketName, CodeInvalidBucketName},
		{ErrInvalidKey, CodeInvalidKey},
		{ErrObjectTooLarge, CodeObjectTooLarge},
		{ErrMetadataTooLarge, CodeMetadataTooLarge},
		{ErrEmptyFile, CodeEmptyFile},
		{ErrAccessDenied, CodeAccessDenied},
		{ErrUnknownAccessKey, CodeUnknownAccessKey},
		{ErrAccessKeyNotFound, CodeAccessKeyNotFound},
		{ErrAccessKeyRevoked, CodeAccessKeyRevoked},
		{ErrSignatureMismatch, CodeSignatureMismatch},
		{ErrRequestExpired, CodeRequestExpired},
		{ErrPreconditionFailed, CodePreconditionFailed},
		{ErrInvalidRange, CodeInvalidRange},
		{ErrBadDigest, CodeBadDigest},
		{ErrNotImplemented, CodeNotImplemented},
		{ErrNoSuchUpload, CodeNoSuchUpload},
		{ErrInvalidPart, CodeInvalidPart},
		{ErrInvalidPartOrder, CodeInvalidPartOrder},
		{ErrEntityTooSmall, CodeEntityTooSmall},
		{ErrUploadBusy, CodeUploadBusy},
		{ErrSlowDown, CodeSlowDown},
		{ErrServiceUnavailable, CodeServiceUnavailable},
		{ErrKeyUnavailable, CodeKeyUnavailable},
		{ErrMissingDependency, CodeMissingDependency},
	}
}

func Code(err error) string {
	if err == nil {
		return ""
	}
	for _, m := range catalogue() {
		if errors.Is(err, m.sentinel) {
			return m.code
		}
	}
	return CodeInternal
}
