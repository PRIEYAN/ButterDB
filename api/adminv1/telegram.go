package adminv1

import (
	"time"
)

type TelegramState string

const (
	TelegramNoCredentials    TelegramState = "no_credentials"
	TelegramReady            TelegramState = "ready"
	TelegramSendingCode      TelegramState = "sending_code"
	TelegramAwaitingCode     TelegramState = "awaiting_code"
	TelegramAwaitingPassword TelegramState = "awaiting_password"
	TelegramLinked           TelegramState = "linked"
)

type TelegramNext string

const (
	NextApp      TelegramNext = "app"
	NextPhone    TelegramNext = "phone"
	NextCode     TelegramNext = "code"
	NextPassword TelegramNext = "password"
	NextWait     TelegramNext = "wait"
	NextNone     TelegramNext = "none"
)

type TelegramStatus struct {
	State       TelegramState       `json:"state"`
	Next        TelegramNext        `json:"next"`
	Credentials TelegramCredentials `json:"credentials"`
	Login       *TelegramLogin      `json:"login"`
	Account     *TelegramAccount    `json:"account"`
	LastError   *ErrorBody          `json:"last_error"`
}

type TelegramCredentials struct {
	APIID      int  `json:"api_id"`
	APIHashSet bool `json:"api_hash_set"`
}

type TelegramLogin struct {
	LoginID              string       `json:"login_id"`
	PhoneMasked          string       `json:"phone_masked"`
	CodeDelivery         CodeDelivery `json:"code_delivery"`
	PasswordHint         string       `json:"password_hint"`
	CodeAttemptsLeft     int          `json:"code_attempts_left"`
	PasswordAttemptsLeft int          `json:"password_attempts_left"`
	StartedAt            time.Time    `json:"started_at"`
	ExpiresAt            time.Time    `json:"expires_at"`
}

type CodeDelivery struct {
	Type           string `json:"type"`
	Length         int    `json:"length"`
	NextType       string `json:"next_type"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type TelegramAccount struct {
	UserID      int64     `json:"user_id"`
	Username    string    `json:"username"`
	FirstName   string    `json:"first_name"`
	PhoneMasked string    `json:"phone_masked"`
	LinkedAt    time.Time `json:"linked_at"`
	Connected   bool      `json:"connected"`
}

type TelegramCredentialsRequest struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

type TelegramLoginStartRequest struct {
	Phone string `json:"phone"`
}

type TelegramCodeRequest struct {
	LoginID string `json:"login_id"`
	Code    string `json:"code"`
}

type TelegramPasswordRequest struct {
	LoginID  string `json:"login_id"`
	Password string `json:"password"`
}
