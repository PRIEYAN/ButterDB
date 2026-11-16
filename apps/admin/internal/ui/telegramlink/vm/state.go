package vm

import (
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Step string

const (
	StepApp      Step = "app"
	StepPhone    Step = "phone"
	StepWait     Step = "wait"
	StepCode     Step = "code"
	StepPassword Step = "password"
	StepDone     Step = "done"
)

type Field string

const (
	FieldNone     Field = ""
	FieldAPIID    Field = "api_id"
	FieldAPIHash  Field = "api_hash"
	FieldPhone    Field = "phone"
	FieldCode     Field = "code"
	FieldPassword Field = "password"
)

const (
	WaitPollInterval = 2 * time.Second
	IdlePollInterval = 10 * time.Second
)

type State struct {
	Loading        bool
	Problem        *model.Problem
	ProblemSeq     int
	Step           Step
	Credentials    adminv1.TelegramCredentials
	Login          *adminv1.TelegramLogin
	Account        *adminv1.TelegramAccount
	SubmitDisabled bool
	FloodUntil     time.Time
	Focus          Field
	FocusSeq       int
}

func stepForNext(next adminv1.TelegramNext) (Step, bool) {
	switch next {
	case adminv1.NextApp:
		return StepApp, true
	case adminv1.NextPhone:
		return StepPhone, true
	case adminv1.NextWait:
		return StepWait, true
	case adminv1.NextCode:
		return StepCode, true
	case adminv1.NextPassword:
		return StepPassword, true
	case adminv1.NextNone:
		return StepDone, true
	}
	return "", false
}

func firstField(step Step) Field {
	switch step {
	case StepApp:
		return FieldAPIID
	case StepPhone:
		return FieldPhone
	case StepCode:
		return FieldCode
	case StepPassword:
		return FieldPassword
	}
	return FieldNone
}
