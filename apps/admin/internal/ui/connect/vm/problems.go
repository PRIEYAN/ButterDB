package vm

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	FieldURL        = "url"
	FieldSetupToken = "setup_token"
	FieldDeviceName = "device_name"
	FieldPairCode   = "pairing_code"

	textBadURL            = "Enter the server address as host or host:port."
	textPlainHTTP         = "Plain http is allowed only for localhost, 127.0.0.1 and ::1. Use https."
	textSetupTokenFormat  = "Setup tokens start with bdst1_. Paste the token the server logged."
	textDeviceNameInvalid = "Use 1 to 64 characters with no control characters."
	textPairingCodeShort  = "Enter all 10 characters of the code."
	textPairingCodeRetry  = "Request a new one if it expired."
	textPinDisagrees      = "The server reports a different certificate than the one presented. A proxy or interceptor is in the path."
	textServerIDMissing   = "The server did not report an identity."
	textServerIDChanged   = "This address now answers as a different server. Nothing was changed."
	textRotateNotSaved    = "Could not save the rotated token; this device must re-pair within 10 minutes."
	textRotateFailed      = "Could not rotate the device token. It will be tried again on the next connect."
	textNoTokenStored     = "This device has no stored sign-in for %s. Pair it again."
	textSignedOut         = "This device is no longer signed in to %s."
	textRevokedAt         = "This device was revoked at %s."
	textRevoked           = "This device was revoked."
)

func versionMismatchText(version string) string {
	return fmt.Sprintf("This server speaks admin API %s; update butterDB Admin.", version)
}

func inlineProblem(field, title string) *model.Problem {
	return &model.Problem{
		Code:         adminv1.CodeValidationFailed,
		Title:        title,
		Field:        field,
		Presentation: model.PresentInline,
	}
}

func dialogProblem(title string) *model.Problem {
	return &model.Problem{Title: title, Presentation: model.PresentDialog}
}

func describe(err error, now time.Time) *model.Problem {
	p := model.Describe(err, now)
	if p.Code == "" && p.Title == "" {
		return nil
	}
	return &p
}

func describeCode(code adminv1.Code, now time.Time) *model.Problem {
	return describe(&adminv1.APIError{Status: 400, Body: adminv1.ErrorBody{Code: code}}, now)
}

func urlProblem(err error) *model.Problem {
	if errors.Is(err, ErrPlainHTTPNotLoopback) {
		return inlineProblem(FieldURL, textPlainHTTP)
	}
	return inlineProblem(FieldURL, textBadURL)
}

func validDeviceName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len([]rune(trimmed)) > maxDeviceNameLength {
		return false
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func revokedNotice(err error) string {
	var apiErr *adminv1.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		if at, ok := apiErr.Body.Details["revoked_at"].(string); ok && at != "" {
			return fmt.Sprintf(textRevokedAt, at)
		}
	}
	return textRevoked
}
