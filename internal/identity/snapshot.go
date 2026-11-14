package identity

import "time"

type State string

const (
	StateNoCredentials    State = "no_credentials"
	StateReady            State = "ready"
	StateSendingCode      State = "sending_code"
	StateAwaitingCode     State = "awaiting_code"
	StateAwaitingPassword State = "awaiting_password"
	StateLinked           State = "linked"
)

type Next string

const (
	NextApp      Next = "app"
	NextPhone    Next = "phone"
	NextCode     Next = "code"
	NextPassword Next = "password"
	NextWait     Next = "wait"
	NextNone     Next = "none"
)

type CredentialsSummary struct {
	APIID      int
	APIHashSet bool
}

type CodeDelivery struct {
	Type           string
	Length         int
	NextType       string
	TimeoutSeconds int
}

type LoginSummary struct {
	LoginID              string
	PhoneMasked          string
	CodeDelivery         CodeDelivery
	PasswordHint         string
	CodeAttemptsLeft     int
	PasswordAttemptsLeft int
	StartedAt            time.Time
	ExpiresAt            time.Time
}

type AccountSummary struct {
	UserID      int64
	Username    string
	FirstName   string
	PhoneMasked string
	LinkedAt    time.Time
}

type Snapshot struct {
	State       State
	Credentials CredentialsSummary
	Login       *LoginSummary
	Account     *AccountSummary
	LastError   *Failure
}

func (s Snapshot) Next() Next {
	switch s.State {
	case StateNoCredentials:
		return NextApp
	case StateReady:
		return NextPhone
	case StateSendingCode:
		return NextWait
	case StateAwaitingCode:
		return NextCode
	case StateAwaitingPassword:
		return NextPassword
	}
	return NextNone
}
