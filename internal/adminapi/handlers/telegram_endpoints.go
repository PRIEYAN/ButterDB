package handlers

import (
	"net/http"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/identity"
)

const (
	maxTelegramAPIID  = 2147483647
	telegramHashChars = 32
	minPhoneDigits    = 6
	maxPhoneDigits    = 14
	maxCodeChars      = 16
	maxLoginIDChars   = 128
	maxPasswordBytes  = 1024
)

func (a *api) registerTelegramRoutes() {
	a.authed("GET "+adminv1.PathSetupTelegram, a.telegramStatus)
	a.authedRelay("POST "+adminv1.PathSetupTelegramApp, a.telegramSaveApp)
	a.authedRelay("DELETE "+adminv1.PathSetupTelegramApp, a.telegramDeleteApp)
	a.authedRelay("POST "+adminv1.PathSetupTelegramPhone, a.telegramPhone)
	a.authedRelay("POST "+adminv1.PathSetupTelegramCode, a.telegramCode)
	a.authedRelay("POST "+adminv1.PathSetupTelegramPassword, a.telegramPassword)
	a.authedRelay("POST "+adminv1.PathSetupTelegramCancel, a.telegramCancel)
	a.authedRelay("POST "+adminv1.PathSetupTelegramUnlink, a.telegramUnlink)
}

func (a *api) writeTelegram(w http.ResponseWriter, r *http.Request, snap identity.Snapshot) {
	a.writeJSON(w, r, http.StatusOK, telegramStatusDTO(snap))
}

func (a *api) telegramStatus(w http.ResponseWriter, r *http.Request) error {
	snap, err := a.deps.Setup.Relay().Status(r.Context())
	if err != nil {
		return err
	}
	a.writeTelegram(w, r, snap)
	return nil
}

func (a *api) telegramSaveApp(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.TelegramCredentialsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.APIID < 1 || req.APIID > maxTelegramAPIID {
		return &ValidationError{Field: "api_id", Reason: "out of range"}
	}
	hash := strings.ToLower(req.APIHash)
	if !isLowerHex(hash, telegramHashChars) {
		return &ValidationError{Field: "api_hash", Reason: "must be 32 hexadecimal characters"}
	}
	relay := a.deps.Setup.Relay()
	err := relay.SaveCredentials(r.Context(), req.APIID, hash)
	a.record(r, "telegram.credentials_save", "", err)
	if err != nil {
		return err
	}
	return a.respondWithStatus(w, r, relay)
}

func (a *api) telegramDeleteApp(w http.ResponseWriter, r *http.Request) error {
	relay := a.deps.Setup.Relay()
	err := relay.DeleteCredentials(r.Context())
	a.record(r, "telegram.credentials_delete", "", err)
	if err != nil {
		return err
	}
	return a.respondWithStatus(w, r, relay)
}

func (a *api) respondWithStatus(w http.ResponseWriter, r *http.Request, relay setup.LoginRelay) error {
	snap, err := relay.Status(r.Context())
	if err != nil {
		return err
	}
	a.writeTelegram(w, r, snap)
	return nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func normalisePhone(raw string) (string, error) {
	cleaned := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(raw)
	if len(cleaned) < 2 || cleaned[0] != '+' || cleaned[1] < '1' || cleaned[1] > '9' {
		return "", failure(adminv1.CodeTelegramPhoneInvalid, nil)
	}
	digits := cleaned[2:]
	if len(digits) < minPhoneDigits || len(digits) > maxPhoneDigits {
		return "", failure(adminv1.CodeTelegramPhoneInvalid, nil)
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", failure(adminv1.CodeTelegramPhoneInvalid, nil)
		}
	}
	return cleaned, nil
}

func (a *api) telegramPhone(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.TelegramLoginStartRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	phone, err := normalisePhone(req.Phone)
	if err != nil {
		return err
	}
	snap, err := a.deps.Setup.Relay().Start(r.Context(), phone)
	a.record(r, "telegram.login_start", maskedPhone(snap), err)
	if err != nil {
		return err
	}
	a.writeTelegram(w, r, snap)
	return nil
}

func maskedPhone(snap identity.Snapshot) string {
	if snap.Login == nil {
		return ""
	}
	return snap.Login.PhoneMasked
}

func (a *api) telegramCode(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.TelegramCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if len(code) < 1 || len(code) > maxCodeChars {
		return &ValidationError{Field: "code", Reason: "length out of range"}
	}
	if err := checkLoginID(req.LoginID); err != nil {
		return err
	}
	snap, err := a.deps.Setup.Relay().SubmitCode(r.Context(), req.LoginID, code)
	return a.finishLoginStep(w, r, snap, err)
}

func (a *api) telegramPassword(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.TelegramPasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if len(req.Password) < 1 || len(req.Password) > maxPasswordBytes {
		return &ValidationError{Field: "password", Reason: "length out of range"}
	}
	if err := checkLoginID(req.LoginID); err != nil {
		return err
	}
	snap, err := a.deps.Setup.Relay().SubmitPassword(r.Context(), req.LoginID, req.Password)
	return a.finishLoginStep(w, r, snap, err)
}

func checkLoginID(loginID string) error {
	if loginID == "" || len(loginID) > maxLoginIDChars {
		return &ValidationError{Field: "login_id", Reason: "length out of range"}
	}
	return nil
}

func (a *api) finishLoginStep(w http.ResponseWriter, r *http.Request, snap identity.Snapshot, err error) error {
	if err != nil {
		a.record(r, "telegram.login_failure", "", err)
		return err
	}
	if snap.State == identity.StateLinked {
		a.record(r, "telegram.login_success", "", nil)
	}
	a.writeTelegram(w, r, snap)
	return nil
}

func (a *api) telegramCancel(w http.ResponseWriter, r *http.Request) error {
	snap, err := a.deps.Setup.Relay().Cancel(r.Context())
	a.record(r, "telegram.login_cancel", "", err)
	if err != nil {
		return err
	}
	a.writeTelegram(w, r, snap)
	return nil
}

func (a *api) telegramUnlink(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.UnlinkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.Confirm != adminv1.UnlinkConfirmation {
		return &ValidationError{Field: "confirm", Reason: "must be unlink"}
	}
	snap, err := a.deps.Setup.Relay().Unlink(r.Context())
	a.record(r, "telegram.unlink", "", err)
	if err != nil {
		return err
	}
	a.writeTelegram(w, r, snap)
	return nil
}
