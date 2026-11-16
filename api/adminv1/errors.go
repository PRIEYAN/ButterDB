package adminv1

import (
	"errors"
	"strconv"
	"time"
)

type Code string

const (
	CodeBadRequest                   Code = "bad_request"
	CodeValidationFailed             Code = "validation_failed"
	CodeUnauthenticated              Code = "unauthenticated"
	CodeDeviceRevoked                Code = "device_revoked"
	CodeNotFound                     Code = "not_found"
	CodeMethodNotAllowed             Code = "method_not_allowed"
	CodeMisdirectedRequest           Code = "misdirected_request"
	CodeLengthRequired               Code = "length_required"
	CodePayloadTooLarge              Code = "payload_too_large"
	CodeUnsupportedMediaType         Code = "unsupported_media_type"
	CodeRateLimited                  Code = "rate_limited"
	CodeInternal                     Code = "internal"
	CodeServerNotReady               Code = "server_not_ready"
	CodeNotImplemented               Code = "not_implemented"
	CodeAlreadyClaimed               Code = "already_claimed"
	CodeNotClaimed                   Code = "not_claimed"
	CodeSetupTokenInvalid            Code = "setup_token_invalid"
	CodePairingCodeInvalid           Code = "pairing_code_invalid"
	CodePairingUnavailable           Code = "pairing_unavailable"
	CodeLastDevice                   Code = "last_device"
	CodeDeviceNotFound               Code = "device_not_found"
	CodeTelegramNotLinked            Code = "telegram_not_linked"
	CodeTelegramUnavailable          Code = "telegram_unavailable"
	CodeTelegramFloodWait            Code = "telegram_flood_wait"
	CodeTelegramCredentialsMissing   Code = "telegram_credentials_missing"
	CodeTelegramAlreadyLinked        Code = "telegram_already_linked"
	CodeTelegramLoginInProgress      Code = "telegram_login_in_progress"
	CodeTelegramLoginNotActive       Code = "telegram_login_not_active"
	CodeTelegramAPIIDInvalid         Code = "telegram_api_id_invalid"
	CodeTelegramPhoneInvalid         Code = "telegram_phone_invalid"
	CodeTelegramPhoneBanned          Code = "telegram_phone_banned"
	CodeTelegramAccountNotRegistered Code = "telegram_account_not_registered"
	CodeTelegramCodeInvalid          Code = "telegram_code_invalid"
	CodeTelegramCodeExpired          Code = "telegram_code_expired"
	CodeTelegramPasswordInvalid      Code = "telegram_password_invalid"
	CodeTelegramAttemptsExhausted    Code = "telegram_attempts_exhausted"
	CodeTelegramEmailSetupRequired   Code = "telegram_email_setup_required"
	CodeTelegramAccountMismatch      Code = "telegram_account_mismatch"
	CodeTelegramLoginFailed          Code = "telegram_login_failed"
	CodeBucketNotFound               Code = "bucket_not_found"
	CodeBucketExists                 Code = "bucket_exists"
	CodeBucketNameInvalid            Code = "bucket_name_invalid"
	CodeBucketNotEmpty               Code = "bucket_not_empty"
	CodeBucketLimitReached           Code = "bucket_limit_reached"
	CodeAccessKeyNotFound            Code = "access_key_not_found"
	CodeScopeInvalid                 Code = "scope_invalid"
	CodeObjectNotFound               Code = "object_not_found"
	CodeObjectKeyInvalid             Code = "object_key_invalid"
	CodeObjectTooLarge               Code = "object_too_large"
	CodeMetadataTooLarge             Code = "metadata_too_large"
	CodeChecksumMismatch             Code = "checksum_mismatch"
	CodePreconditionFailed           Code = "precondition_failed"
	CodeRangeNotSatisfiable          Code = "range_not_satisfiable"
	CodeIdempotencyConflict          Code = "idempotency_conflict"
	CodeCommitOutcomeUnknown         Code = "commit_outcome_unknown"
	CodePresignKeyInvalid            Code = "presign_key_invalid"
	CodeS3PublicURLUnset             Code = "s3_public_url_unset"
	CodeReplicaNotLinked             Code = "replica_not_linked"
	CodeReplicaAlreadyLinked         Code = "replica_already_linked"
	CodeReplicaSameAccount           Code = "replica_same_account"
)

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code              Code           `json:"code"`
	Message           string         `json:"message"`
	Retryable         bool           `json:"retryable"`
	Hint              string         `json:"hint"`
	RetryAfterSeconds int            `json:"retry_after_seconds,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
	RequestID         string         `json:"request_id"`
}

type APIError struct {
	Status int
	Body   ErrorBody
}

func (e *APIError) Error() string {
	if e == nil {
		return "adminv1: <nil>"
	}
	return "adminv1: " + strconv.Itoa(e.Status) + " " + string(e.Body.Code) + ": " + e.Body.Message
}

func IsCode(err error, code Code) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	return apiErr.Body.Code == code
}

func RetryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.Body.RetryAfterSeconds <= 0 {
		return 0, false
	}
	return time.Duration(apiErr.Body.RetryAfterSeconds) * time.Second, true
}
