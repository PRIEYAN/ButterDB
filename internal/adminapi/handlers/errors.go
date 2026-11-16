package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var (
	ErrAlreadyClaimed     = errors.New("handlers: server already claimed")
	ErrNotClaimed         = errors.New("handlers: server not claimed")
	ErrSetupTokenInvalid  = errors.New("handlers: setup token invalid")
	ErrPairingCodeInvalid = errors.New("handlers: pairing code invalid")
	ErrPairingUnavailable = errors.New("handlers: pairing delivery unavailable")
	ErrLastDevice         = errors.New("handlers: revoking the last active device")
	ErrDeviceNotFound     = errors.New("handlers: device not found")
	ErrTelegramNotLinked  = errors.New("handlers: telegram not linked")
	ErrServerNotReady     = errors.New("handlers: server not ready")
	ErrS3PublicURLUnset   = errors.New("handlers: s3 public url not configured")
	ErrReplicaNotLinked   = errors.New("handlers: replica not linked")
	ErrReplicaLinked      = errors.New("handlers: replica already linked")
)

type RateLimitedError struct {
	Scope      string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return "handlers: rate limited: " + e.Scope
}

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return "handlers: validation failed: " + e.Field
}

type BadRequestError struct {
	Field string
}

func (e *BadRequestError) Error() string {
	return "handlers: bad request: " + e.Field
}

type DigestMismatchError struct {
	Algorithm string
}

func (e *DigestMismatchError) Error() string {
	return "handlers: " + e.Algorithm + " digest mismatch"
}

func (e *DigestMismatchError) Is(target error) bool {
	return target == errs.ErrBadDigest
}

const (
	defaultFloodWaitRetry      = 5 * time.Second
	uncertainOutcomeRetry      = 30 * time.Second
	serverNotReadyRetry        = 5 * time.Second
	validationDetailField      = "field"
	validationDetailReason     = "reason"
	telegramAttemptsLeftDetail = "attempts_left"
)

type codeInfo struct {
	code      adminv1.Code
	status    int
	retryable bool
	message   string
	hint      string
}

func codeTable() []codeInfo {
	return []codeInfo{
		{adminv1.CodeBadRequest, http.StatusBadRequest, false, "The request is malformed.", "Fix the request and send it again."},
		{adminv1.CodeValidationFailed, http.StatusUnprocessableEntity, false, "The request breaks a validation rule.", "Correct the named field."},
		{adminv1.CodeUnauthenticated, http.StatusUnauthorized, false, "A valid device token is required.", "Pair this device again."},
		{adminv1.CodeDeviceRevoked, http.StatusUnauthorized, false, "This device has been revoked.", "Pair this device again."},
		{adminv1.CodeNotFound, http.StatusNotFound, false, "The resource was not found.", ""},
		{adminv1.CodeMethodNotAllowed, http.StatusMethodNotAllowed, false, "The method is not allowed for this route.", ""},
		{adminv1.CodeMisdirectedRequest, http.StatusMisdirectedRequest, false, "The Host header is not accepted by this server.", "Connect with a configured host name."},
		{adminv1.CodeLengthRequired, http.StatusLengthRequired, false, "Content-Length is required.", "Send the upload with a Content-Length header."},
		{adminv1.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, false, "The request body is too large.", ""},
		{adminv1.CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, false, "The request must be application/json.", "Set Content-Type to application/json."},
		{adminv1.CodeRateLimited, http.StatusTooManyRequests, true, "Too many requests.", "Wait and try again."},
		{adminv1.CodeInternal, http.StatusInternalServerError, false, "The server failed to complete the request.", "Check the server log with the request id."},
		{adminv1.CodeServerNotReady, http.StatusServiceUnavailable, true, "The server is not ready.", "Try again shortly."},
		{adminv1.CodeNotImplemented, http.StatusNotImplemented, false, "This endpoint is not available yet.", ""},
		{adminv1.CodeAlreadyClaimed, http.StatusConflict, false, "This server has already been claimed.", "Pair this device with a pairing code."},
		{adminv1.CodeNotClaimed, http.StatusConflict, false, "This server has not been claimed.", "Claim the server with the setup token."},
		{adminv1.CodeSetupTokenInvalid, http.StatusUnauthorized, false, "The setup token is not valid.", "Run butterdbd admin-token on the host."},
		{adminv1.CodePairingCodeInvalid, http.StatusUnauthorized, false, "The pairing code is not valid.", "Request a new pairing code."},
		{adminv1.CodePairingUnavailable, http.StatusConflict, false, "The pairing delivery method is not available.", "Use butterdbd admin-token on the host."},
		{adminv1.CodeLastDevice, http.StatusConflict, false, "This is the only active device.", "Confirm to revoke the last device."},
		{adminv1.CodeDeviceNotFound, http.StatusNotFound, false, "The device does not exist.", ""},
		{adminv1.CodeTelegramNotLinked, http.StatusConflict, false, "No Telegram account is linked.", "Link a Telegram account first."},
		{adminv1.CodeTelegramUnavailable, http.StatusServiceUnavailable, true, "Telegram is not reachable.", "Try again shortly."},
		{adminv1.CodeTelegramFloodWait, http.StatusServiceUnavailable, true, "Telegram asked the server to wait.", "Retry after the indicated delay."},
		{adminv1.CodeTelegramCredentialsMissing, http.StatusConflict, false, "Telegram api_id and api_hash are not saved.", "Save the Telegram app credentials first."},
		{adminv1.CodeTelegramAlreadyLinked, http.StatusConflict, false, "A Telegram account is already linked.", "Unlink it before changing credentials."},
		{adminv1.CodeTelegramLoginInProgress, http.StatusConflict, false, "A Telegram login is already in progress.", "Finish or cancel the active attempt."},
		{adminv1.CodeTelegramLoginNotActive, http.StatusConflict, false, "No matching Telegram login attempt is active.", "Start a new login."},
		{adminv1.CodeTelegramAPIIDInvalid, http.StatusUnprocessableEntity, false, "Telegram rejected the api_id or api_hash.", "Check the credentials at my.telegram.org."},
		{adminv1.CodeTelegramPhoneInvalid, http.StatusUnprocessableEntity, false, "The phone number is not valid.", "Use international format such as +447700900134."},
		{adminv1.CodeTelegramPhoneBanned, http.StatusForbidden, false, "Telegram has banned this phone number.", ""},
		{adminv1.CodeTelegramAccountNotRegistered, http.StatusUnprocessableEntity, false, "No Telegram account uses this phone number.", "Register in the Telegram app first."},
		{adminv1.CodeTelegramCodeInvalid, http.StatusUnprocessableEntity, true, "Telegram rejected the login code.", "Check the code in your Telegram app and enter it again."},
		{adminv1.CodeTelegramCodeExpired, http.StatusGone, false, "The login code has expired.", "Start a new login."},
		{adminv1.CodeTelegramPasswordInvalid, http.StatusUnprocessableEntity, true, "Telegram rejected the two-step password.", "Enter the password again."},
		{adminv1.CodeTelegramAttemptsExhausted, http.StatusConflict, false, "Too many invalid attempts.", "Start a new login."},
		{adminv1.CodeTelegramEmailSetupRequired, http.StatusUnprocessableEntity, false, "Telegram requires login email setup, which is not supported.", "Set up a login email in the Telegram app first."},
		{adminv1.CodeTelegramAccountMismatch, http.StatusConflict, false, "The login is for a different Telegram account than the one bound.", "Log in with the bound account."},
		{adminv1.CodeTelegramLoginFailed, http.StatusBadGateway, false, "The Telegram login failed.", "Start a new login."},
		{adminv1.CodeBucketNotFound, http.StatusNotFound, false, "The bucket does not exist.", ""},
		{adminv1.CodeBucketExists, http.StatusConflict, false, "The bucket already exists.", ""},
		{adminv1.CodeBucketNameInvalid, http.StatusUnprocessableEntity, false, "The bucket name is not valid.", "Use lowercase letters, digits, dots and hyphens, 3 to 63 characters."},
		{adminv1.CodeBucketNotEmpty, http.StatusConflict, false, "The bucket still holds objects.", "Delete the objects first."},
		{adminv1.CodeBucketLimitReached, http.StatusConflict, false, "The bucket limit has been reached.", "Delete a bucket first."},
		{adminv1.CodeAccessKeyNotFound, http.StatusNotFound, false, "The access key does not exist.", ""},
		{adminv1.CodeScopeInvalid, http.StatusUnprocessableEntity, false, "The access key scope is not valid.", "List existing buckets and at least one of read, list, write, delete."},
		{adminv1.CodeObjectNotFound, http.StatusNotFound, false, "The object does not exist.", ""},
		{adminv1.CodeObjectKeyInvalid, http.StatusUnprocessableEntity, false, "The object key is not valid.", "Use a non-empty UTF-8 key of at most 1024 bytes."},
		{adminv1.CodeObjectTooLarge, http.StatusRequestEntityTooLarge, false, "The object exceeds the size limit.", ""},
		{adminv1.CodeMetadataTooLarge, http.StatusUnprocessableEntity, false, "The user metadata exceeds the size limit.", ""},
		{adminv1.CodeChecksumMismatch, http.StatusBadRequest, false, "The body does not match the supplied checksum.", "Send the upload again."},
		{adminv1.CodePreconditionFailed, http.StatusPreconditionFailed, false, "A precondition failed.", ""},
		{adminv1.CodeRangeNotSatisfiable, http.StatusRequestedRangeNotSatisfiable, false, "The requested range cannot be satisfied.", ""},
		{adminv1.CodeIdempotencyConflict, http.StatusConflict, false, "The Idempotency-Key was used for a different request.", "Use a new Idempotency-Key."},
		{adminv1.CodeCommitOutcomeUnknown, http.StatusServiceUnavailable, true, "The Telegram commit outcome is unknown.", "Retry with the same Idempotency-Key."},
		{adminv1.CodePresignKeyInvalid, http.StatusUnprocessableEntity, false, "The access key cannot sign this URL.", "Choose an active key with read access to the bucket."},
		{adminv1.CodeS3PublicURLUnset, http.StatusConflict, false, "The S3 public URL is not configured.", "Set the S3 public URL in the server configuration."},
		{adminv1.CodeReplicaNotLinked, http.StatusConflict, false, "No replica account is linked.", ""},
		{adminv1.CodeReplicaAlreadyLinked, http.StatusConflict, false, "A replica account is already linked.", ""},
		{adminv1.CodeReplicaSameAccount, http.StatusConflict, false, "The replica login is the primary account.", "Log in with a different Telegram account."},
	}
}

func lookupCode(code adminv1.Code) codeInfo {
	for _, info := range codeTable() {
		if info.code == code {
			return info
		}
	}
	return lookupCode(adminv1.CodeInternal)
}

func apiError(code adminv1.Code, details map[string]any) adminv1.APIError {
	info := lookupCode(code)
	return adminv1.APIError{
		Status: info.status,
		Body: adminv1.ErrorBody{
			Code:      info.code,
			Message:   info.message,
			Retryable: info.retryable,
			Hint:      info.hint,
			Details:   details,
		},
	}
}

func failure(code adminv1.Code, details map[string]any) error {
	e := apiError(code, details)
	return &e
}

func withRetryAfter(e adminv1.APIError, wait time.Duration) adminv1.APIError {
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	e.Body.RetryAfterSeconds = seconds
	return e
}

func MapError(err error) adminv1.APIError {
	if err == nil {
		return apiError(adminv1.CodeInternal, nil)
	}
	var passthrough *adminv1.APIError
	if errors.As(err, &passthrough) && passthrough != nil {
		return *passthrough
	}
	for _, step := range []func(error) (adminv1.APIError, bool){
		mapTyped, mapTransport, mapIdentity, mapAdminSentinel, mapBucket, mapAccess, mapObject, mapIngest, mapErrs,
	} {
		if mapped, ok := step(err); ok {
			return mapped
		}
	}
	return apiError(adminv1.CodeInternal, nil)
}

func mapTyped(err error) (adminv1.APIError, bool) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return apiError(adminv1.CodeValidationFailed, fieldDetails(validation.Field, validation.Reason)), true
	}
	var badRequest *BadRequestError
	if errors.As(err, &badRequest) {
		return apiError(adminv1.CodeBadRequest, fieldDetails(badRequest.Field, "")), true
	}
	var limited *RateLimitedError
	if errors.As(err, &limited) {
		e := apiError(adminv1.CodeRateLimited, map[string]any{"scope": limited.Scope})
		return withRetryAfter(e, limited.RetryAfter), true
	}
	var digest *DigestMismatchError
	if errors.As(err, &digest) {
		return apiError(adminv1.CodeChecksumMismatch, map[string]any{"algorithm": digest.Algorithm}), true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return apiError(adminv1.CodePayloadTooLarge, map[string]any{"limit_bytes": tooLarge.Limit}), true
	}
	var unsatisfiable *httprange.UnsatisfiableRangeError
	if errors.As(err, &unsatisfiable) {
		return apiError(adminv1.CodeRangeNotSatisfiable, map[string]any{"size_bytes": unsatisfiable.Size}), true
	}
	var precondition *httprange.PreconditionError
	if errors.As(err, &precondition) {
		return apiError(adminv1.CodePreconditionFailed, nil), true
	}
	return adminv1.APIError{}, false
}

func fieldDetails(field, reason string) map[string]any {
	details := map[string]any{}
	if field != "" {
		details[validationDetailField] = field
	}
	if reason != "" {
		details[validationDetailReason] = reason
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func mapTransport(err error) (adminv1.APIError, bool) {
	var flood *tgclient.FloodWaitError
	if errors.As(err, &flood) {
		return floodWait(flood.Duration), true
	}
	var slow *admission.SlowDownError
	if errors.As(err, &slow) {
		return floodWait(slow.RetryAfter), true
	}
	var unknown *put.OutcomeUnknownError
	if errors.As(err, &unknown) {
		e := apiError(adminv1.CodeCommitOutcomeUnknown, map[string]any{"job_id": unknown.JobID})
		return withRetryAfter(e, uncertainOutcomeRetry), true
	}
	switch {
	case errors.Is(err, put.ErrCommitOutcomeUnknown),
		errors.Is(err, put.ErrControlProjection),
		errors.Is(err, tgclient.ErrSendOutcomeUnknown):
		return withRetryAfter(apiError(adminv1.CodeCommitOutcomeUnknown, nil), uncertainOutcomeRetry), true
	case errors.Is(err, tgclient.ErrFloodWait):
		return floodWait(defaultFloodWaitRetry), true
	case errors.Is(err, tgclient.ErrNotAuthorized):
		return apiError(adminv1.CodeTelegramNotLinked, nil), true
	case errors.Is(err, tgclient.ErrChannelUnavailable):
		return apiError(adminv1.CodeTelegramUnavailable, nil), true
	}
	return adminv1.APIError{}, false
}

func floodWait(wait time.Duration) adminv1.APIError {
	return withRetryAfter(apiError(adminv1.CodeTelegramFloodWait, nil), wait)
}

func mapIdentity(err error) (adminv1.APIError, bool) {
	var step *identity.StepError
	details := map[string]any{}
	if errors.As(err, &step) {
		if step.AttemptsLeft > 0 {
			details[telegramAttemptsLeftDetail] = step.AttemptsLeft
		}
		if step.BoundUserID != 0 {
			details["bound_user_id"] = step.BoundUserID
		}
	}
	for _, entry := range identityCodes() {
		if errors.Is(err, entry.sentinel) {
			if len(details) == 0 {
				details = nil
			}
			return apiError(entry.code, details), true
		}
	}
	return adminv1.APIError{}, false
}

type sentinelCode struct {
	sentinel error
	code     adminv1.Code
}

func identityCodes() []sentinelCode {
	return []sentinelCode{
		{identity.ErrCredentialsMissing, adminv1.CodeTelegramCredentialsMissing},
		{identity.ErrCredentialsInvalid, adminv1.CodeTelegramAPIIDInvalid},
		{identity.ErrCredentialsCorrupt, adminv1.CodeTelegramCredentialsMissing},
		{identity.ErrAlreadyLinked, adminv1.CodeTelegramAlreadyLinked},
		{identity.ErrLoginInProgress, adminv1.CodeTelegramLoginInProgress},
		{identity.ErrLoginNotActive, adminv1.CodeTelegramLoginNotActive},
		{identity.ErrPhoneInvalid, adminv1.CodeTelegramPhoneInvalid},
		{identity.ErrCodeInvalid, adminv1.CodeTelegramCodeInvalid},
		{identity.ErrCodeExpired, adminv1.CodeTelegramCodeExpired},
		{identity.ErrPasswordInvalid, adminv1.CodeTelegramPasswordInvalid},
		{identity.ErrAttemptsExhausted, adminv1.CodeTelegramAttemptsExhausted},
		{identity.ErrAccountMismatch, adminv1.CodeTelegramAccountMismatch},
		{identity.ErrReplicaSameAccount, adminv1.CodeReplicaSameAccount},
		{identity.ErrTelegramUnavailable, adminv1.CodeTelegramUnavailable},
		{identity.ErrSessionNotFound, adminv1.CodeTelegramNotLinked},
		{identity.ErrSessionUnseal, adminv1.CodeInternal},
		{identity.ErrSessionKeyMissing, adminv1.CodeInternal},
		{identity.ErrLoginFailed, adminv1.CodeTelegramLoginFailed},
	}
}

func mapAdminSentinel(err error) (adminv1.APIError, bool) {
	entries := []sentinelCode{
		{ErrAlreadyClaimed, adminv1.CodeAlreadyClaimed},
		{ErrNotClaimed, adminv1.CodeNotClaimed},
		{ErrSetupTokenInvalid, adminv1.CodeSetupTokenInvalid},
		{ErrPairingCodeInvalid, adminv1.CodePairingCodeInvalid},
		{ErrPairingUnavailable, adminv1.CodePairingUnavailable},
		{ErrLastDevice, adminv1.CodeLastDevice},
		{ErrDeviceNotFound, adminv1.CodeDeviceNotFound},
		{ErrTelegramNotLinked, adminv1.CodeTelegramNotLinked},
		{ErrS3PublicURLUnset, adminv1.CodeS3PublicURLUnset},
		{ErrReplicaNotLinked, adminv1.CodeReplicaNotLinked},
		{ErrReplicaLinked, adminv1.CodeReplicaAlreadyLinked},
	}
	for _, entry := range entries {
		if errors.Is(err, entry.sentinel) {
			return apiError(entry.code, nil), true
		}
	}
	if errors.Is(err, ErrServerNotReady) {
		return withRetryAfter(apiError(adminv1.CodeServerNotReady, nil), serverNotReadyRetry), true
	}
	return adminv1.APIError{}, false
}

func mapBucket(err error) (adminv1.APIError, bool) {
	switch {
	case errors.Is(err, buckets.ErrBucketLimitReached):
		return apiError(adminv1.CodeBucketLimitReached, map[string]any{"limit": limits.MaxBuckets}), true
	case errors.Is(err, bucket.ErrInvalidBucketName):
		return apiError(adminv1.CodeBucketNameInvalid, nil), true
	case errors.Is(err, bucket.ErrInvalidPolicy):
		return apiError(adminv1.CodeValidationFailed, fieldDetails("policy", "must be private or public-read")), true
	case errors.Is(err, bucket.ErrInvalidEncryption):
		return apiError(adminv1.CodeValidationFailed, fieldDetails("encryption_default", "must be none or tde1")), true
	case errors.Is(err, bucket.ErrInvalidCacheMaxAge):
		return apiError(adminv1.CodeValidationFailed, fieldDetails("cache_max_age_seconds", "must be 0 to 31536000")), true
	}
	return adminv1.APIError{}, false
}

func mapAccess(err error) (adminv1.APIError, bool) {
	switch {
	case errors.Is(err, access.ErrInvalidScope), errors.Is(err, access.ErrInvalidAction):
		return apiError(adminv1.CodeScopeInvalid, fieldDetails("scope", "")), true
	case errors.Is(err, access.ErrInvalidAccessKeyID):
		return apiError(adminv1.CodeAccessKeyNotFound, nil), true
	case errors.Is(err, access.ErrInvalidKeyName):
		return apiError(adminv1.CodeValidationFailed, fieldDetails("name", "must be 1 to 64 characters")), true
	}
	return adminv1.APIError{}, false
}

func mapObject(err error) (adminv1.APIError, bool) {
	switch {
	case errors.Is(err, object.ErrMetadataTooLarge):
		return apiError(adminv1.CodeMetadataTooLarge, map[string]any{"max_bytes": object.MaxUserMetadataBytes}), true
	case errors.Is(err, object.ErrInvalidKey), errors.Is(err, errs.ErrInvalidKey):
		return apiError(adminv1.CodeObjectKeyInvalid, objectKeyReason(err)), true
	case errors.Is(err, object.ErrInvalidMetadataName), errors.Is(err, object.ErrInvalidMetadataText):
		return apiError(adminv1.CodeValidationFailed, fieldDetails("metadata", "")), true
	}
	return adminv1.APIError{}, false
}

func objectKeyReason(err error) map[string]any {
	switch {
	case errors.Is(err, object.ErrKeyEmpty):
		return map[string]any{validationDetailReason: "empty"}
	case errors.Is(err, object.ErrKeyTooLong):
		return map[string]any{validationDetailReason: "too_long"}
	case errors.Is(err, object.ErrKeyNotUTF8):
		return map[string]any{validationDetailReason: "not_utf8"}
	case errors.Is(err, object.ErrKeyControlCharacter):
		return map[string]any{validationDetailReason: "control_character"}
	}
	return nil
}

func mapIngest(err error) (adminv1.APIError, bool) {
	switch {
	case errors.Is(err, put.ErrIdempotencyConflict):
		return apiError(adminv1.CodeIdempotencyConflict, nil), true
	case errors.Is(err, put.ErrInvalidRequest):
		return apiError(adminv1.CodeValidationFailed, nil), true
	case errors.Is(err, spool.ErrLengthMismatch):
		return apiError(adminv1.CodeBadRequest, fieldDetails("Content-Length", "body length differs from Content-Length")), true
	}
	return adminv1.APIError{}, false
}

func mapErrs(err error) (adminv1.APIError, bool) {
	for _, entry := range errsCodes() {
		if errors.Is(err, entry.sentinel) {
			return entry.build(), true
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apiError(adminv1.CodeTelegramUnavailable, nil), true
	}
	return adminv1.APIError{}, false
}

type errsEntry struct {
	sentinel error
	build    func() adminv1.APIError
}

func plainCode(code adminv1.Code) func() adminv1.APIError {
	return func() adminv1.APIError { return apiError(code, nil) }
}

func errsCodes() []errsEntry {
	return []errsEntry{
		{errs.ErrNoSuchBucket, plainCode(adminv1.CodeBucketNotFound)},
		{errs.ErrNoSuchKey, plainCode(adminv1.CodeObjectNotFound)},
		{errs.ErrBucketExists, plainCode(adminv1.CodeBucketExists)},
		{errs.ErrBucketNotEmpty, plainCode(adminv1.CodeBucketNotEmpty)},
		{errs.ErrInvalidBucketName, plainCode(adminv1.CodeBucketNameInvalid)},
		{errs.ErrInvalidKey, plainCode(adminv1.CodeObjectKeyInvalid)},
		{errs.ErrObjectTooLarge, objectTooLarge},
		{errs.ErrMetadataTooLarge, metadataTooLarge},
		{errs.ErrEmptyFile, emptyFile},
		{errs.ErrAccessDenied, plainCode(adminv1.CodeUnauthenticated)},
		{errs.ErrUnknownAccessKey, plainCode(adminv1.CodeAccessKeyNotFound)},
		{errs.ErrAccessKeyNotFound, plainCode(adminv1.CodeAccessKeyNotFound)},
		{errs.ErrAccessKeyRevoked, plainCode(adminv1.CodeAccessKeyNotFound)},
		{errs.ErrSignatureMismatch, plainCode(adminv1.CodeInternal)},
		{errs.ErrRequestExpired, plainCode(adminv1.CodeInternal)},
		{errs.ErrPreconditionFailed, plainCode(adminv1.CodePreconditionFailed)},
		{errs.ErrInvalidRange, plainCode(adminv1.CodeRangeNotSatisfiable)},
		{errs.ErrBadDigest, badDigest},
		{errs.ErrNoSuchUpload, plainCode(adminv1.CodeInternal)},
		{errs.ErrInvalidPart, plainCode(adminv1.CodeInternal)},
		{errs.ErrInvalidPartOrder, plainCode(adminv1.CodeInternal)},
		{errs.ErrEntityTooSmall, plainCode(adminv1.CodeInternal)},
		{errs.ErrUploadBusy, plainCode(adminv1.CodeInternal)},
		{errs.ErrSlowDown, slowDown},
		{errs.ErrServiceUnavailable, plainCode(adminv1.CodeTelegramUnavailable)},
		{errs.ErrNotImplemented, notImplemented},
		{errs.ErrKeyUnavailable, plainCode(adminv1.CodeInternal)},
		{errs.ErrMissingDependency, plainCode(adminv1.CodeInternal)},
	}
}

func objectTooLarge() adminv1.APIError {
	return apiError(adminv1.CodeObjectTooLarge, map[string]any{"max_bytes": int64(limits.MaxStoredObjectBytes)})
}

func metadataTooLarge() adminv1.APIError {
	return apiError(adminv1.CodeMetadataTooLarge, map[string]any{"max_bytes": object.MaxUserMetadataBytes})
}

func emptyFile() adminv1.APIError {
	return apiError(adminv1.CodeValidationFailed, fieldDetails("body", "empty objects cannot be uploaded"))
}

func badDigest() adminv1.APIError {
	return apiError(adminv1.CodeChecksumMismatch, map[string]any{"algorithm": "content"})
}

func slowDown() adminv1.APIError {
	return floodWait(defaultFloodWaitRetry)
}

func notImplemented() adminv1.APIError {
	return apiError(adminv1.CodeNotImplemented, nil)
}
