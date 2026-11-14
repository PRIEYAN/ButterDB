package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestSystemStatusNeedsSetupWhenNoCreds(t *testing.T) {
	f := newFixture(t)
	snap, err := f.svc.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if snap.State != StateNoCredentials || snap.Next() != NextApp || snap.Credentials.APIHashSet {
		t.Fatalf("snapshot = %+v, want no_credentials", snap)
	}
}

func TestSystemStatusReadyWhenCredsExist(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	snap, err := f.svc.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if snap.State != StateReady || snap.Next() != NextPhone || snap.Credentials.APIID != 1234567 || !snap.Credentials.APIHashSet {
		t.Fatalf("snapshot = %+v, want ready with credentials", snap)
	}
}

func TestCorruptCredentialsFileIsAnError(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	if err := writeFile(f.svc.credentialsPath, "{not json"); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if _, err := f.svc.Status(context.Background()); !errors.Is(err, ErrCredentialsCorrupt) {
		t.Fatalf("status error = %v, want ErrCredentialsCorrupt", err)
	}
}

func TestCredentialsRoundTripAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		apiID   int
		apiHash string
		wantErr error
	}{
		{"valid", 1234567, testAPIHash, nil},
		{"upper case hash is lowered", 1234567, strings.ToUpper(testAPIHash), nil},
		{"zero id", 0, testAPIHash, ErrCredentialsInvalid},
		{"id too large", 2147483648, testAPIHash, ErrCredentialsInvalid},
		{"short hash", 1, "abc", ErrCredentialsInvalid},
		{"non hex hash", 1, strings.Repeat("g", 32), ErrCredentialsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			err := f.svc.SaveCredentials(context.Background(), tt.apiID, tt.apiHash)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("save error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			creds, err := f.svc.LoadCredentials(context.Background())
			if err != nil || creds.APIID != tt.apiID || creds.APIHash != testAPIHash {
				t.Fatalf("load = %+v, %v", creds, err)
			}
			if runtime.GOOS == "windows" {
				return
			}
			info, err := os.Stat(f.svc.credentialsPath)
			if err != nil {
				t.Fatalf("stat credentials: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("credentials mode = %04o, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestDeleteCredentialsRemovesFile(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	if err := f.svc.DeleteCredentials(context.Background()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.svc.DeleteCredentials(context.Background()); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := f.svc.LoadCredentials(context.Background()); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("load error = %v, want ErrCredentialsMissing", err)
	}
}

func TestStartRequiresCredentialsAndValidPhone(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Start(context.Background(), testPhone); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("start without credentials error = %v", err)
	}
	f.saveCredentials(t)
	if _, err := f.svc.Start(context.Background(), "12345"); !errors.Is(err, ErrPhoneInvalid) {
		t.Fatalf("start with bad phone error = %v", err)
	}
	if f.auth.sendCalls != 0 {
		t.Fatalf("sendCode calls = %d, want 0", f.auth.sendCalls)
	}
}

func TestStartMovesToAwaitingCode(t *testing.T) {
	f := newFixture(t)
	snap := f.startLogin(t)
	if snap.State != StateAwaitingCode || snap.Next() != NextCode || snap.Login == nil {
		t.Fatalf("snapshot = %+v, want awaiting_code", snap)
	}
	login := snap.Login
	if !strings.HasPrefix(login.LoginID, "lg_") || len(login.LoginID) != 15 {
		t.Fatalf("login id = %q", login.LoginID)
	}
	if login.PhoneMasked != "+44 ••• •• 34" || login.CodeAttemptsLeft != 5 || login.PasswordAttemptsLeft != 5 {
		t.Fatalf("login = %+v", login)
	}
	if login.ExpiresAt.Sub(login.StartedAt) != DefaultAttemptTTL || login.CodeDelivery.TimeoutSeconds != 60 {
		t.Fatalf("login timing = %+v", login)
	}
}

func TestSecondStartIsRefusedWhileAttemptActive(t *testing.T) {
	f := newFixture(t)
	f.startLogin(t)
	if _, err := f.svc.Start(context.Background(), testPhone); !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("second start error = %v, want ErrLoginInProgress", err)
	}
	if err := f.svc.SaveCredentials(context.Background(), 1, testAPIHash); !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("save during attempt error = %v, want ErrLoginInProgress", err)
	}
}

func TestStartSendCodeFailureReturnsToReady(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	f.auth.sendCode = func(context.Context, string) (tgclient.SentCode, error) {
		return tgclient.SentCode{}, &tgclient.FloodWaitError{Duration: 30 * time.Second}
	}
	snap, err := f.svc.Start(context.Background(), testPhone)
	var wait *tgclient.FloodWaitError
	if !errors.As(err, &wait) || wait.Duration != 30*time.Second {
		t.Fatalf("start error = %v, want flood wait", err)
	}
	if snap.State != StateReady || snap.LastError == nil || snap.LastError.Code != CodeFloodWait || snap.LastError.RetryAfter != 30*time.Second {
		t.Fatalf("snapshot = %+v, want ready with flood wait error", snap)
	}
}

func TestStartTimeoutIsTelegramUnavailable(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	f.auth.sendCode = func(ctx context.Context, _ string) (tgclient.SentCode, error) {
		<-ctx.Done()
		return tgclient.SentCode{}, ctx.Err()
	}
	f.svc.stepTimeout = 20 * time.Millisecond
	snap, err := f.svc.Start(context.Background(), testPhone)
	if !errors.Is(err, ErrTelegramUnavailable) {
		t.Fatalf("start error = %v, want ErrTelegramUnavailable", err)
	}
	if snap.State != StateReady {
		t.Fatalf("state = %s, want ready", snap.State)
	}
}

func TestCodeRejectedKeepsAttemptAndCountsDown(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPhoneCodeInvalid
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, "11111")
	var step *StepError
	if !errors.Is(err, ErrCodeInvalid) || !errors.As(err, &step) || step.AttemptsLeft != 4 {
		t.Fatalf("submit error = %v, want invalid code with 4 attempts left", err)
	}
	if snap.State != StateAwaitingCode || snap.Login.CodeAttemptsLeft != 4 || snap.LastError != nil {
		t.Fatalf("snapshot = %+v, want awaiting_code, 4 left, no terminal error", snap)
	}
}

func TestCodeInvalidRetryInPlace(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.auth.signIn = func(_ context.Context, _, _, code string) (tgclient.Account, error) {
		calls++
		if code != testLoginCode {
			return tgclient.Account{}, tgclient.ErrPhoneCodeInvalid
		}
		return tgclient.Account{UserID: testUserID, FirstName: "Ada", Username: "ada"}, nil
	}
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitCode(context.Background(), loginID, "00000"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("first submit error = %v", err)
	}
	snap, err := f.svc.SubmitCode(context.Background(), loginID, " "+testLoginCode+" ")
	if err != nil || snap.State != StateLinked {
		t.Fatalf("second submit = %+v, %v; want linked", snap, err)
	}
	if f.auth.sendCalls != 1 {
		t.Fatalf("sendCode calls = %d, want 1: a retry must not request a new code", f.auth.sendCalls)
	}
	if calls != 2 || f.auth.signInHash[0] != testCodeHash || f.auth.signInHash[1] != testCodeHash {
		t.Fatalf("sign in hashes = %v, want the same hash twice", f.auth.signInHash)
	}
	if f.auth.signInCodes[1] != testLoginCode {
		t.Fatalf("trimmed code = %q", f.auth.signInCodes[1])
	}
}

func TestCodeRetryDeliversNewCodeAfterRejection(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(_ context.Context, _, _, code string) (tgclient.Account, error) {
		if code == "11111" {
			return tgclient.Account{}, tgclient.ErrPhoneCodeInvalid
		}
		return tgclient.Account{UserID: testUserID}, nil
	}
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitCode(context.Background(), loginID, "11111"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("first submit error = %v", err)
	}
	snap, err := f.svc.SubmitCode(context.Background(), loginID, "22222")
	if err != nil || snap.State != StateLinked {
		t.Fatalf("retry = %+v, %v; want linked", snap, err)
	}
	if f.auth.signInCodes[1] != "22222" {
		t.Fatalf("delivered codes = %v", f.auth.signInCodes)
	}
}

func TestFifthInvalidCodeEndsAttempt(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPhoneCodeInvalid
	}
	loginID := f.startLogin(t).Login.LoginID
	var snap Snapshot
	var err error
	for i := 0; i < MaxCodeAttempts; i++ {
		snap, err = f.svc.SubmitCode(context.Background(), loginID, "00000")
	}
	if !errors.Is(err, ErrAttemptsExhausted) {
		t.Fatalf("fifth submit error = %v, want ErrAttemptsExhausted", err)
	}
	if snap.State != StateReady || snap.LastError == nil || snap.LastError.Code != CodeAttemptsExhausted {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestExpiredCodeEndsAttempt(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPhoneCodeExpired
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if !errors.Is(err, ErrCodeExpired) || snap.State != StateReady || snap.LastError.Code != CodeCodeExpired {
		t.Fatalf("submit = %+v, %v", snap, err)
	}
}

func TestFloodWaitDuringCodeKeepsStage(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, &tgclient.FloodWaitError{Duration: time.Minute}
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if FailureOf(err).Code != CodeFloodWait || snap.State != StateAwaitingCode {
		t.Fatalf("submit = %+v, %v; want flood wait and awaiting_code", snap, err)
	}
}

func TestUnknownCodeFailureEndsAttemptWithCause(t *testing.T) {
	f := newFixture(t)
	cause := errors.New("rpc error code 400")
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, cause
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if !errors.Is(err, ErrLoginFailed) || !errors.Is(err, cause) || snap.State != StateReady {
		t.Fatalf("submit = %+v, %v", snap, err)
	}
}

func TestPanicInFlowEndsAttemptAsLoginFailed(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		panic("boom")
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if !errors.Is(err, ErrLoginFailed) || snap.State != StateReady || snap.LastError.Code != CodeLoginFailed {
		t.Fatalf("submit = %+v, %v", snap, err)
	}
}

func TestSubmitInWrongStageIsRejectedNotBuffered(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	if _, err := f.svc.SubmitPassword(context.Background(), "lg_none", "secret"); !errors.Is(err, ErrLoginNotActive) {
		t.Fatalf("password without attempt error = %v", err)
	}
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitPassword(context.Background(), loginID, "secret"); !errors.Is(err, ErrLoginNotActive) {
		t.Fatalf("password while awaiting code error = %v", err)
	}
	if _, err := f.svc.SubmitCode(context.Background(), "lg_other", testLoginCode); !errors.Is(err, ErrLoginNotActive) {
		t.Fatalf("code with wrong login id error = %v", err)
	}
	if len(f.auth.signInCodes) != 0 {
		t.Fatal("a rejected submit reached telegram")
	}
}

func TestPasswordFlowWithHint(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPasswordNeeded
	}
	f.auth.hint = func(context.Context) (string, error) { return "first pet", nil }
	attempts := 0
	f.auth.checkPass = func(_ context.Context, password string) (tgclient.Account, error) {
		attempts++
		if password != "right" {
			return tgclient.Account{}, tgclient.ErrPasswordInvalid
		}
		return tgclient.Account{UserID: testUserID, Username: "ada"}, nil
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if err != nil || snap.State != StateAwaitingPassword || snap.Next() != NextPassword || snap.Login.PasswordHint != "first pet" {
		t.Fatalf("code submit = %+v, %v", snap, err)
	}
	snap, err = f.svc.SubmitPassword(context.Background(), loginID, "wrong")
	var step *StepError
	if !errors.Is(err, ErrPasswordInvalid) || !errors.As(err, &step) || step.AttemptsLeft != 4 || snap.State != StateAwaitingPassword {
		t.Fatalf("wrong password = %+v, %v", snap, err)
	}
	snap, err = f.svc.SubmitPassword(context.Background(), loginID, "right")
	if err != nil || snap.State != StateLinked || attempts != 2 {
		t.Fatalf("right password = %+v, %v after %d attempts", snap, err, attempts)
	}
}

func TestPasswordHintBestEffort(t *testing.T) {
	tests := []struct {
		name     string
		hint     func(context.Context) (string, error)
		wantHint string
	}{
		{"hint available", func(context.Context) (string, error) { return "birthday", nil }, "birthday"},
		{"lookup fails", func(context.Context) (string, error) { return "", errors.New("rpc down") }, ""},
		{"lookup panics", func(context.Context) (string, error) { panic("rpc") }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
				return tgclient.Account{}, tgclient.ErrPasswordNeeded
			}
			f.auth.hint = tt.hint
			loginID := f.startLogin(t).Login.LoginID
			snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
			if err != nil {
				t.Fatalf("submit error = %v, want none", err)
			}
			if snap.State != StateAwaitingPassword || snap.Login.PasswordHint != tt.wantHint {
				t.Fatalf("snapshot = %+v, want awaiting_password with hint %q", snap, tt.wantHint)
			}
		})
	}
}

func TestFifthInvalidPasswordEndsAttempt(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPasswordNeeded
	}
	f.auth.checkPass = func(context.Context, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPasswordInvalid
	}
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode); err != nil {
		t.Fatalf("code: %v", err)
	}
	var err error
	for i := 0; i < MaxPasswordAttempts; i++ {
		_, err = f.svc.SubmitPassword(context.Background(), loginID, "x")
	}
	if !errors.Is(err, ErrAttemptsExhausted) {
		t.Fatalf("error = %v, want ErrAttemptsExhausted", err)
	}
}

func TestLinkRecordsAccountBinding(t *testing.T) {
	f := newFixture(t)
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if snap.State != StateLinked || snap.Next() != NextNone || snap.Login != nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	account := snap.Account
	if account.UserID != testUserID || account.Username != "ada" || account.FirstName != "Ada" || account.PhoneMasked != "+44 ••• •• 34" {
		t.Fatalf("account = %+v", account)
	}
	row, found, _ := f.accounts.Get(context.Background(), "primary")
	if !found || !row.Linked || row.UserID != testUserID || row.PhoneMasked != "+44 ••• •• 34" {
		t.Fatalf("row = %+v, %v", row, found)
	}
	if _, err := f.svc.Start(context.Background(), testPhone); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("start while linked error = %v", err)
	}
	if err := f.svc.SaveCredentials(context.Background(), 1, testAPIHash); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("save credentials while linked error = %v", err)
	}
}

func TestLinkResolvesUserThroughSelfWhenMissing(t *testing.T) {
	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, nil
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if err != nil || snap.Account == nil || snap.Account.UserID != testUserID {
		t.Fatalf("submit = %+v, %v", snap, err)
	}
}

func TestLoginAsDifferentUserIsRefusedAndSessionDiscarded(t *testing.T) {
	f := newFixture(t)
	if err := f.accounts.Link(context.Background(), StoredAccount{Role: "primary", UserID: otherUserID, Linked: false, BoundAt: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	loginID := f.startLogin(t).Login.LoginID
	snap, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	var step *StepError
	if !errors.Is(err, ErrAccountMismatch) || !errors.As(err, &step) || step.BoundUserID != otherUserID {
		t.Fatalf("submit error = %v, want account mismatch", err)
	}
	if snap.State != StateReady || snap.LastError.Code != CodeAccountMismatch || snap.LastError.BoundUserID != otherUserID {
		t.Fatalf("snapshot = %+v", snap)
	}
	if f.auth.logOutCalls != 1 || f.sessions.calls != 1 {
		t.Fatalf("logout calls %d, session discards %d; want 1 each", f.auth.logOutCalls, f.sessions.calls)
	}
}

func TestReplicaCannotUsePrimaryAccount(t *testing.T) {
	accounts := newMemAccounts()
	if err := accounts.Link(context.Background(), StoredAccount{Role: "primary", UserID: testUserID, Linked: true, BoundAt: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f := newFixtureIn(t, t.TempDir(), accounts, RoleReplica)
	loginID := f.startLogin(t).Login.LoginID
	_, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	if !errors.Is(err, ErrReplicaSameAccount) {
		t.Fatalf("submit error = %v, want ErrReplicaSameAccount", err)
	}
}

func TestTwoRolesCoexist(t *testing.T) {
	accounts := newMemAccounts()
	primary := newFixtureIn(t, t.TempDir(), accounts, RolePrimary)
	replica := newFixtureIn(t, t.TempDir(), accounts, RoleReplica)
	replica.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{UserID: otherUserID}, nil
	}
	for _, f := range []*loginFixture{primary, replica} {
		loginID := f.startLogin(t).Login.LoginID
		if _, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode); err != nil {
			t.Fatalf("%s submit: %v", f.svc.Role(), err)
		}
	}
	if _, ok, _ := accounts.Get(context.Background(), "replica"); !ok {
		t.Fatal("replica row missing")
	}
	if ok, _ := primary.svc.Linked(context.Background()); !ok {
		t.Fatal("primary not linked")
	}
}

func TestCancelEndsAttemptAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.startLogin(t)
	for i := 0; i < 2; i++ {
		snap, err := f.svc.Cancel(context.Background())
		if err != nil || snap.State != StateReady {
			t.Fatalf("cancel %d = %+v, %v", i, snap, err)
		}
	}
}

func TestCancelAbortsInFlightStep(t *testing.T) {
	f := newFixture(t)
	f.saveCredentials(t)
	entered := make(chan struct{})
	f.auth.sendCode = func(ctx context.Context, _ string) (tgclient.SentCode, error) {
		close(entered)
		<-ctx.Done()
		return tgclient.SentCode{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Start(context.Background(), testPhone)
		done <- err
	}()
	<-entered
	if _, err := f.svc.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrLoginNotActive) {
			t.Fatalf("start error = %v, want ErrLoginNotActive", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start did not return after cancel")
	}
}

func TestAttemptExpiresAfterFifteenMinutes(t *testing.T) {
	f := newFixture(t)
	loginID := f.startLogin(t).Login.LoginID
	f.clock.Advance(DefaultAttemptTTL - time.Second)
	if snap, _ := f.svc.Status(context.Background()); snap.State != StateAwaitingCode {
		t.Fatalf("state before expiry = %s", snap.State)
	}
	f.clock.Advance(time.Second)
	snap, _ := f.svc.Status(context.Background())
	if snap.State != StateReady {
		t.Fatalf("state after expiry = %s, want ready", snap.State)
	}
	if _, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode); !errors.Is(err, ErrLoginNotActive) {
		t.Fatalf("submit after expiry error = %v", err)
	}
}

func TestUnlinkKeepsBindingAndCredentials(t *testing.T) {
	f := newFixture(t)
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode); err != nil {
		t.Fatalf("submit: %v", err)
	}
	snap, err := f.svc.Unlink(context.Background())
	if err != nil || snap.State != StateReady || snap.LastError != nil {
		t.Fatalf("unlink = %+v, %v", snap, err)
	}
	if f.auth.logOutCalls != 1 || f.sessions.calls != 1 {
		t.Fatalf("logout calls %d, session discards %d", f.auth.logOutCalls, f.sessions.calls)
	}
	row, found, _ := f.accounts.Get(context.Background(), "primary")
	if !found || row.Linked || row.UserID != testUserID || row.UnlinkedAt == 0 {
		t.Fatalf("row = %+v", row)
	}
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{UserID: otherUserID}, nil
	}
	next, err := f.svc.Start(context.Background(), testPhone)
	if err != nil {
		t.Fatalf("relink start: %v", err)
	}
	if _, err := f.svc.SubmitCode(context.Background(), next.Login.LoginID, testLoginCode); !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("relink as another user error = %v, want mismatch", err)
	}
}

func TestUnlinkLogoutFailureIsReportedButContinues(t *testing.T) {
	f := newFixture(t)
	loginID := f.startLogin(t).Login.LoginID
	if _, err := f.svc.SubmitCode(context.Background(), loginID, testLoginCode); err != nil {
		t.Fatalf("submit: %v", err)
	}
	f.auth.logOut = func(context.Context) error { return errors.New("offline") }
	snap, err := f.svc.Unlink(context.Background())
	if err != nil || snap.State != StateReady || snap.LastError == nil || snap.LastError.Code != CodeUnavailable {
		t.Fatalf("unlink = %+v, %v", snap, err)
	}
	if f.sessions.calls != 1 {
		t.Fatal("session was not discarded after logout failure")
	}
}

func TestLoginStateSurvivesServiceRecreation(t *testing.T) {
	accounts := newMemAccounts()
	dir := t.TempDir()
	first := newFixtureIn(t, dir, accounts, RolePrimary)
	loginID := first.startLogin(t).Login.LoginID
	if _, err := first.svc.SubmitCode(context.Background(), loginID, testLoginCode); err != nil {
		t.Fatalf("submit: %v", err)
	}
	second := newFixtureIn(t, dir, accounts, RolePrimary)
	snap, err := second.svc.Status(context.Background())
	if err != nil || snap.State != StateLinked || snap.Account.UserID != testUserID || snap.Credentials.APIID != 1234567 {
		t.Fatalf("status after restart = %+v, %v", snap, err)
	}
}

func TestSnapshotHasNoSecrets(t *testing.T) {
	assertNoSecretFields(t, reflect.TypeOf(Snapshot{}), "Snapshot")

	f := newFixture(t)
	f.auth.signIn = func(context.Context, string, string, string) (tgclient.Account, error) {
		return tgclient.Account{}, tgclient.ErrPasswordNeeded
	}
	f.auth.hint = func(context.Context) (string, error) { return "hint", nil }
	snaps := []Snapshot{f.startLogin(t)}
	loginID := snaps[0].Login.LoginID
	next, _ := f.svc.SubmitCode(context.Background(), loginID, testLoginCode)
	snaps = append(snaps, next)
	for _, snap := range snaps {
		rendered := fmt.Sprintf("%+v %#v", snap, snap)
		if snap.Login != nil {
			rendered += fmt.Sprintf("%+v", *snap.Login)
		}
		for _, secret := range []string{testCodeHash, testAPIHash, testPhone, testLoginCode, "447700900134"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("snapshot %s leaks %q: %s", snap.State, secret, rendered)
			}
		}
	}
}

func assertNoSecretFields(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("%s can hold raw bytes", path)
		}
		assertNoSecretFields(t, typ.Elem(), path)
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.ToLower(field.Name)
			if strings.Contains(name, "session") || strings.Contains(name, "codehash") || strings.Contains(name, "phonecodehash") || name == "apihash" {
				t.Fatalf("%s.%s names a secret", path, field.Name)
			}
			assertNoSecretFields(t, field.Type, path+"."+field.Name)
		}
	case reflect.Interface, reflect.Map, reflect.Func, reflect.Chan:
		t.Fatalf("%s has an open-ended %s field", path, typ.Kind())
	}
}

func TestNewServiceRejectsNilDependencies(t *testing.T) {
	base := newFixture(t)
	valid := Dependencies{
		Role:            RolePrimary,
		CredentialsPath: base.svc.credentialsPath,
		Writer:          base.svc.writer,
		Reader:          osFiles{},
		Remover:         osFiles{},
		Auth:            base.auth,
		Self:            base.self,
		Accounts:        base.accounts,
		Sessions:        base.sessions,
		Clock:           clock.NewFake(time.Now()),
		Random:          &counterReader{},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"writer", func(d *Dependencies) { d.Writer = nil }},
		{"reader", func(d *Dependencies) { d.Reader = nil }},
		{"remover", func(d *Dependencies) { d.Remover = nil }},
		{"auth", func(d *Dependencies) { d.Auth = nil }},
		{"self", func(d *Dependencies) { d.Self = nil }},
		{"accounts", func(d *Dependencies) { d.Accounts = nil }},
		{"sessions", func(d *Dependencies) { d.Sessions = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"random", func(d *Dependencies) { d.Random = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
		{"credentials path", func(d *Dependencies) { d.CredentialsPath = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := valid
			tt.mutate(&deps)
			if _, err := NewService(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
	bad := valid
	bad.Role = "other"
	if _, err := NewService(bad); err == nil {
		t.Fatal("invalid role accepted")
	}
	if _, err := NewService(valid); err != nil {
		t.Fatalf("valid deps: %v", err)
	}
}

func TestPhoneNormalizeAndMask(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		masked  string
		wantErr bool
	}{
		{"plain", "+447700900134", "+447700900134", "+44 ••• •• 34", false},
		{"separators removed", "+49 (151) 234-56712", "+4915123456712", "+49 ••• •• 12", false},
		{"one digit code", "+12025550123", "+12025550123", "+1 ••• •• 23", false},
		{"three digit code", "+351912345678", "+351912345678", "+351 ••• •• 78", false},
		{"missing plus", "447700900134", "", "", true},
		{"leading zero", "+0447700900", "", "", true},
		{"too short", "+123456", "", "", true},
		{"too long", "+1234567890123456", "", "", true},
		{"letters", "+44770090013x", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePhone(tt.raw)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("NormalizePhone(%q) = %q, %v", tt.raw, got, err)
			}
			if !tt.wantErr && MaskPhone(got) != tt.masked {
				t.Fatalf("MaskPhone(%q) = %q, want %q", got, MaskPhone(got), tt.masked)
			}
		})
	}
}
