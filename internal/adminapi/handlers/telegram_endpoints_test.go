package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func awaitingCodeSnapshot() identity.Snapshot {
	return identity.Snapshot{
		State:       identity.StateAwaitingCode,
		Credentials: identity.CredentialsSummary{APIID: 1234567, APIHashSet: true},
		Login: &identity.LoginSummary{
			LoginID:              "lg_5t2q9w3e8r1y",
			PhoneMasked:          "+44 77•• ••• •34",
			CodeDelivery:         identity.CodeDelivery{Type: "app", Length: 5, NextType: "sms", TimeoutSeconds: 60},
			CodeAttemptsLeft:     5,
			PasswordAttemptsLeft: 5,
			StartedAt:            testStart,
			ExpiresAt:            testStart.Add(15 * time.Minute),
		},
	}
}

func TestTelegramStatusEndpoint(t *testing.T) {
	h := newHarness(t)
	h.setup.relay.snapshot = awaitingCodeSnapshot()
	w := h.do(request{method: http.MethodGet, path: adminv1.PathSetupTelegram})
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	status := decodeBody[adminv1.TelegramStatus](t, w)
	if status.State != adminv1.TelegramAwaitingCode || status.Next != adminv1.NextCode || !status.Credentials.APIHashSet || status.Credentials.APIID != 1234567 {
		t.Fatalf("status = %+v", status)
	}
	if status.Login == nil || status.Login.LoginID != "lg_5t2q9w3e8r1y" || status.Login.CodeDelivery.Length != 5 || status.Account != nil {
		t.Fatalf("login = %+v account %+v", status.Login, status.Account)
	}
	for _, field := range []string{`"api_hash"`, "phone_code_hash", "session"} {
		if strings.Contains(w.Body.String(), field) {
			t.Fatalf("status body mentions %s: %s", field, w.Body.String())
		}
	}
}

func TestTelegramStatusLinkedAndLastError(t *testing.T) {
	h := newHarness(t)
	h.setup.relay.snapshot = identity.Snapshot{
		State:     identity.StateLinked,
		Account:   &identity.AccountSummary{UserID: 777000123, Username: "owner", FirstName: "Own", PhoneMasked: "+44 77•• ••• •34", LinkedAt: testStart},
		LastError: &identity.Failure{Code: identity.CodeCodeInvalid, AttemptsLeft: 4},
	}
	w := h.do(request{method: http.MethodGet, path: adminv1.PathSetupTelegram})
	status := decodeBody[adminv1.TelegramStatus](t, w)
	if status.Next != adminv1.NextNone || status.Account == nil || status.Account.UserID != 777000123 || status.Login != nil {
		t.Fatalf("status = %+v", status)
	}
	if status.LastError == nil || status.LastError.Code != adminv1.CodeTelegramCodeInvalid || status.LastError.Details["attempts_left"] != float64(4) || !status.LastError.Retryable {
		t.Fatalf("last_error = %+v", status.LastError)
	}
}

func TestTelegramSaveCredentials(t *testing.T) {
	good := adminv1.TelegramCredentialsRequest{APIID: 1234567, APIHash: "0123456789ABCDEF0123456789abcdef"}
	t.Run("hash is lowercased and audited without secrets", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateReady}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramApp, body: good})
		if w.Code != 200 {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if h.setup.relay.apiID != 1234567 || h.setup.relay.apiHash != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("relay got %d %q", h.setup.relay.apiID, h.setup.relay.apiHash)
		}
		if strings.Join(h.setup.relay.calls, ",") != "save,status" {
			t.Fatalf("calls = %v", h.setup.relay.calls)
		}
		for _, rec := range h.audit.records {
			if strings.Contains(rec.Target, "0123456789") || rec.Action != "telegram.credentials_save" {
				t.Fatalf("audit = %+v", rec)
			}
		}
	})
	invalid := []struct {
		name  string
		req   adminv1.TelegramCredentialsRequest
		field string
	}{
		{"zero api id", adminv1.TelegramCredentialsRequest{APIID: 0, APIHash: good.APIHash}, "api_id"},
		{"huge api id", adminv1.TelegramCredentialsRequest{APIID: 2147483648, APIHash: good.APIHash}, "api_id"},
		{"short hash", adminv1.TelegramCredentialsRequest{APIID: 1, APIHash: "abc"}, "api_hash"},
		{"non hex hash", adminv1.TelegramCredentialsRequest{APIID: 1, APIHash: strings.Repeat("z", 32)}, "api_hash"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramApp, body: test.req})
			if got := assertError(t, w, 422, adminv1.CodeValidationFailed).Details["field"]; got != test.field {
				t.Fatalf("field = %v, want %s", got, test.field)
			}
			if len(h.setup.relay.calls) != 0 {
				t.Fatalf("relay called: %v", h.setup.relay.calls)
			}
		})
	}
	t.Run("refused while linked", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.err = identity.ErrAlreadyLinked
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramApp, body: good})
		assertError(t, w, 409, adminv1.CodeTelegramAlreadyLinked)
		if h.audit.records[0].Outcome != "failure" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
	})
	t.Run("delete credentials", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateNoCredentials}
		w := h.do(request{method: http.MethodDelete, path: adminv1.PathSetupTelegramApp})
		if w.Code != 200 || strings.Join(h.setup.relay.calls, ",") != "delete,status" {
			t.Fatalf("status %d calls %v", w.Code, h.setup.relay.calls)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "telegram.credentials_delete" {
			t.Fatalf("audit = %v", got)
		}
	})
}

func TestTelegramPhone(t *testing.T) {
	valid := []struct{ name, in, want string }{
		{"plain", "+447700900134", "+447700900134"},
		{"spaces and hyphens", "+44 7700-900 134", "+447700900134"},
		{"parentheses", "+1 (415) 555-0100", "+14155550100"},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.setup.relay.snapshot = awaitingCodeSnapshot()
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramPhone, body: adminv1.TelegramLoginStartRequest{Phone: test.in}})
			if w.Code != 200 || h.setup.relay.phone != test.want {
				t.Fatalf("status %d relay phone %q", w.Code, h.setup.relay.phone)
			}
			if len(h.audit.records) != 1 || h.audit.records[0].Action != "telegram.login_start" || h.audit.records[0].Target != "+44 77•• ••• •34" {
				t.Fatalf("audit = %+v", h.audit.records)
			}
			if strings.Contains(h.audit.records[0].Target, "7700900134") {
				t.Fatal("audit holds the full phone number")
			}
		})
	}
	invalid := []string{"", "447700900134", "+0447700900", "+44", "+4477009001341234", "+44770090abc1", "+44 77 00"}
	for _, in := range invalid {
		t.Run("invalid "+in, func(t *testing.T) {
			h := newHarness(t)
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramPhone, body: adminv1.TelegramLoginStartRequest{Phone: in}})
			assertError(t, w, 422, adminv1.CodeTelegramPhoneInvalid)
			if len(h.setup.relay.calls) != 0 {
				t.Fatalf("relay called: %v", h.setup.relay.calls)
			}
		})
	}
	t.Run("relay failure maps", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.err = &identity.StepError{Err: identity.ErrLoginInProgress}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramPhone, body: adminv1.TelegramLoginStartRequest{Phone: "+447700900134"}})
		assertError(t, w, 409, adminv1.CodeTelegramLoginInProgress)
	})
}

func TestTelegramCodeAndPassword(t *testing.T) {
	t.Run("code trimmed and relayed", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateAwaitingPassword}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramCode, body: adminv1.TelegramCodeRequest{LoginID: "lg_1", Code: " 12345 "}})
		if w.Code != 200 || h.setup.relay.code != "12345" || h.setup.relay.loginID != "lg_1" {
			t.Fatalf("status %d relay %q %q", w.Code, h.setup.relay.loginID, h.setup.relay.code)
		}
		if len(h.audit.records) != 0 {
			t.Fatalf("an intermediate step was audited: %+v", h.audit.records)
		}
	})
	t.Run("invalid code retries in place", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.err = &identity.StepError{Err: identity.ErrCodeInvalid, AttemptsLeft: 4}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramCode, body: adminv1.TelegramCodeRequest{LoginID: "lg_1", Code: "00000"}})
		body := assertError(t, w, 422, adminv1.CodeTelegramCodeInvalid)
		if !body.Retryable || body.Details["attempts_left"] != float64(4) {
			t.Fatalf("body = %+v", body)
		}
		if h.audit.records[0].Action != "telegram.login_failure" || h.audit.records[0].ErrorCode != "telegram_code_invalid" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
		if strings.Contains(w.Body.String(), "00000") {
			t.Fatal("response echoes the submitted code")
		}
	})
	failures := []struct {
		name   string
		err    error
		status int
		code   adminv1.Code
	}{
		{"expired", identity.ErrCodeExpired, 410, adminv1.CodeTelegramCodeExpired},
		{"exhausted", identity.ErrAttemptsExhausted, 409, adminv1.CodeTelegramAttemptsExhausted},
		{"wrong stage", identity.ErrLoginNotActive, 409, adminv1.CodeTelegramLoginNotActive},
		{"mismatch", &identity.StepError{Err: identity.ErrAccountMismatch, BoundUserID: 9}, 409, adminv1.CodeTelegramAccountMismatch},
		{"login failed", &identity.StepError{Err: identity.ErrLoginFailed}, 502, adminv1.CodeTelegramLoginFailed},
		{"flood wait", &tgclient.FloodWaitError{Duration: 30 * time.Second}, 503, adminv1.CodeTelegramFloodWait},
	}
	for _, test := range failures {
		t.Run("code "+test.name, func(t *testing.T) {
			h := newHarness(t)
			h.setup.relay.err = test.err
			w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramCode, body: adminv1.TelegramCodeRequest{LoginID: "lg_1", Code: "12345"}})
			assertError(t, w, test.status, test.code)
		})
	}
	t.Run("code validation", func(t *testing.T) {
		h := newHarness(t)
		for _, req := range []adminv1.TelegramCodeRequest{{LoginID: "lg_1", Code: "  "}, {LoginID: "lg_1", Code: strings.Repeat("1", 17)}, {LoginID: "", Code: "1"}} {
			assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramCode, body: req}), 422, adminv1.CodeValidationFailed)
		}
		if len(h.setup.relay.calls) != 0 {
			t.Fatalf("relay called: %v", h.setup.relay.calls)
		}
	})
	t.Run("password success links and audits", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateLinked, Account: &identity.AccountSummary{UserID: 1}}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramPassword, body: adminv1.TelegramPasswordRequest{LoginID: "lg_1", Password: "hunter2"}})
		if w.Code != 200 || h.setup.relay.password != "hunter2" {
			t.Fatalf("status %d", w.Code)
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Action != "telegram.login_success" {
			t.Fatalf("audit = %+v", h.audit.records)
		}
		if strings.Contains(w.Body.String(), "hunter2") {
			t.Fatal("response echoes the password")
		}
	})
	t.Run("password invalid retries in place", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.err = &identity.StepError{Err: identity.ErrPasswordInvalid, AttemptsLeft: 3}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramPassword, body: adminv1.TelegramPasswordRequest{LoginID: "lg_1", Password: "wrong-password"}})
		body := assertError(t, w, 422, adminv1.CodeTelegramPasswordInvalid)
		if !body.Retryable || body.Details["attempts_left"] != float64(3) {
			t.Fatalf("body = %+v", body)
		}
		for _, rec := range h.audit.records {
			if strings.Contains(rec.Target+rec.Action+rec.ErrorCode, "wrong-password") {
				t.Fatalf("audit leaks the password: %+v", rec)
			}
		}
	})
}

func TestTelegramCancelAndUnlink(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateReady}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramCancel})
		if w.Code != 200 || decodeBody[adminv1.TelegramStatus](t, w).State != adminv1.TelegramReady {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "telegram.login_cancel" {
			t.Fatalf("audit = %v", got)
		}
	})
	t.Run("unlink requires confirmation", func(t *testing.T) {
		h := newHarness(t)
		for _, confirm := range []string{"", "yes", "UNLINK"} {
			assertError(t, h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramUnlink, body: adminv1.UnlinkRequest{Confirm: confirm}}), 422, adminv1.CodeValidationFailed)
		}
		if len(h.setup.relay.calls) != 0 {
			t.Fatalf("relay called: %v", h.setup.relay.calls)
		}
	})
	t.Run("unlink", func(t *testing.T) {
		h := newHarness(t)
		h.setup.relay.snapshot = identity.Snapshot{State: identity.StateReady}
		w := h.do(request{method: http.MethodPost, path: adminv1.PathSetupTelegramUnlink, body: adminv1.UnlinkRequest{Confirm: adminv1.UnlinkConfirmation}})
		if w.Code != 200 || strings.Join(h.setup.relay.calls, ",") != "unlink" {
			t.Fatalf("status %d calls %v", w.Code, h.setup.relay.calls)
		}
		if got := h.audit.actions(); len(got) != 1 || got[0] != "telegram.unlink" {
			t.Fatalf("audit = %v", got)
		}
	})
}
