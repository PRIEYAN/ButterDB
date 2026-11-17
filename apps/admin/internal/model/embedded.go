package model

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

const (
	FieldProjectName     = "project_name"
	FieldPassword        = "password"
	FieldConfirmPassword = "confirm_password"
	FieldOldPassword     = "old_password"
	FieldConfirmation    = "confirmation"
)

const (
	textWrongPassword    = "That password does not open this project."
	textProjectName      = "Project names are 1 to 64 characters."
	textConfirmationText = "Type " + embedded.ResetConfirmation + " to confirm."
	textNotLoggedIn      = "Log in to Telegram first."
	textProjectNotReady  = "This project cannot be opened on this device."
	textProjectNotFound  = "That project no longer exists."
	textAlreadyRunning   = "butterDB is already open"
	textPasswordMismatch = "The two passwords do not match."
)

var textPasswordTooShort = "Use at least " + strconv.Itoa(embedded.MinPasswordChars) + " characters."

func describeEmbedded(err error) (Problem, bool) {
	switch {
	case errors.Is(err, embedded.ErrWrongPassword):
		return InlineProblem(FieldPassword, textWrongPassword), true
	case errors.Is(err, embedded.ErrPasswordTooShort):
		return InlineProblem(FieldPassword, textPasswordTooShort), true
	case errors.Is(err, embedded.ErrProjectName):
		return InlineProblem(FieldProjectName, textProjectName), true
	case errors.Is(err, embedded.ErrConfirmation):
		return InlineProblem(FieldConfirmation, textConfirmationText), true
	case errors.Is(err, embedded.ErrNotLoggedIn):
		return Problem{Title: textNotLoggedIn, Presentation: PresentRoute, Route: RouteTelegram}, true
	case errors.Is(err, embedded.ErrProjectNotReady):
		return Problem{Title: textProjectNotReady, Presentation: PresentDialog}, true
	case errors.Is(err, embedded.ErrProjectNotFound):
		return Problem{Title: textProjectNotFound, Presentation: PresentDialog}, true
	case errors.Is(err, embedded.ErrAlreadyRunning):
		return Problem{Title: textAlreadyRunning, Presentation: PresentDialog}, true
	}
	return Problem{}, false
}

func InlineProblem(field, title string) Problem {
	return Problem{Title: title, Field: field, Presentation: PresentInline}
}

func CheckProjectName(name string) *Problem {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > embedded.MaxProjectNameChars {
		p := InlineProblem(FieldProjectName, textProjectName)
		return &p
	}
	return nil
}

func CheckNewPassword(password, confirm []byte) *Problem {
	if utf8.RuneCount(password) < embedded.MinPasswordChars {
		p := InlineProblem(FieldPassword, textPasswordTooShort)
		return &p
	}
	if !bytes.Equal(password, confirm) {
		p := InlineProblem(FieldConfirmPassword, textPasswordMismatch)
		return &p
	}
	return nil
}

func CheckResetConfirmation(typed string) *Problem {
	if typed != embedded.ResetConfirmation {
		p := InlineProblem(FieldConfirmation, textConfirmationText)
		return &p
	}
	return nil
}
