package identity

import (
	"errors"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var (
	ErrCredentialsMissing  = errors.New("identity: telegram credentials missing")
	ErrCredentialsInvalid  = errors.New("identity: telegram credentials invalid")
	ErrCredentialsCorrupt  = errors.New("identity: telegram credentials file unreadable")
	ErrAlreadyLinked       = errors.New("identity: telegram account already linked")
	ErrLoginInProgress     = errors.New("identity: telegram login in progress")
	ErrLoginNotActive      = errors.New("identity: telegram login not active")
	ErrPhoneInvalid        = errors.New("identity: phone number invalid")
	ErrCodeInvalid         = errors.New("identity: login code invalid")
	ErrCodeExpired         = errors.New("identity: login code expired")
	ErrPasswordInvalid     = errors.New("identity: two-step password invalid")
	ErrAttemptsExhausted   = errors.New("identity: login attempts exhausted")
	ErrAccountMismatch     = errors.New("identity: telegram account differs from the bound account")
	ErrReplicaSameAccount  = errors.New("identity: account is bound to another role")
	ErrLoginFailed         = errors.New("identity: telegram login failed")
	ErrTelegramUnavailable = errors.New("identity: telegram unavailable")
	ErrSessionNotFound     = errors.New("identity: session not found")
	ErrSessionUnseal       = errors.New("identity: session cannot be unsealed")
	ErrSessionKeyMissing   = errors.New("identity: session key unavailable")
)

type StepError struct {
	Err          error
	Cause        error
	AttemptsLeft int
	BoundUserID  int64
}

func (e *StepError) Error() string {
	return e.Err.Error()
}

func (e *StepError) Unwrap() []error {
	if e.Cause == nil {
		return []error{e.Err}
	}
	return []error{e.Err, e.Cause}
}

type FailureCode string

const (
	CodeCredentialsMissing FailureCode = "telegram_credentials_missing"
	CodeAlreadyLinked      FailureCode = "telegram_already_linked"
	CodeLoginInProgress    FailureCode = "telegram_login_in_progress"
	CodeLoginNotActive     FailureCode = "telegram_login_not_active"
	CodePhoneInvalid       FailureCode = "telegram_phone_invalid"
	CodeCodeInvalid        FailureCode = "telegram_code_invalid"
	CodeCodeExpired        FailureCode = "telegram_code_expired"
	CodePasswordInvalid    FailureCode = "telegram_password_invalid"
	CodeAttemptsExhausted  FailureCode = "telegram_attempts_exhausted"
	CodeAccountMismatch    FailureCode = "telegram_account_mismatch"
	CodeFloodWait          FailureCode = "telegram_flood_wait"
	CodeUnavailable        FailureCode = "telegram_unavailable"
	CodeLoginFailed        FailureCode = "telegram_login_failed"
	CodeReplicaSameAccount FailureCode = "replica_same_account"
	CodeCredentialsInvalid FailureCode = "telegram_api_id_invalid"
)

type Failure struct {
	Code         FailureCode
	RetryAfter   time.Duration
	AttemptsLeft int
	BoundUserID  int64
}

var failureCodes = []struct {
	err  error
	code FailureCode
}{
	{ErrCredentialsMissing, CodeCredentialsMissing},
	{ErrCredentialsInvalid, CodeCredentialsInvalid},
	{ErrAlreadyLinked, CodeAlreadyLinked},
	{ErrLoginInProgress, CodeLoginInProgress},
	{ErrLoginNotActive, CodeLoginNotActive},
	{ErrPhoneInvalid, CodePhoneInvalid},
	{ErrCodeInvalid, CodeCodeInvalid},
	{ErrCodeExpired, CodeCodeExpired},
	{ErrPasswordInvalid, CodePasswordInvalid},
	{ErrAttemptsExhausted, CodeAttemptsExhausted},
	{ErrAccountMismatch, CodeAccountMismatch},
	{ErrReplicaSameAccount, CodeReplicaSameAccount},
	{ErrTelegramUnavailable, CodeUnavailable},
}

func FailureOf(err error) Failure {
	failure := Failure{Code: CodeLoginFailed}
	var wait *tgclient.FloodWaitError
	if errors.As(err, &wait) {
		failure.Code = CodeFloodWait
		failure.RetryAfter = wait.Duration
		return failure
	}
	for _, entry := range failureCodes {
		if errors.Is(err, entry.err) {
			failure.Code = entry.code
			break
		}
	}
	var step *StepError
	if errors.As(err, &step) {
		failure.AttemptsLeft = step.AttemptsLeft
		failure.BoundUserID = step.BoundUserID
	}
	return failure
}
