package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestRelayReturnsInjectedRelay(t *testing.T) {
	f := newLinkFixture(t, plainScript(), nil)
	if f.relay.Relay() != LoginRelay(f.identity) {
		t.Fatal("Relay did not return the injected relay")
	}
}

func TestCodeInvalidRetryInPlace(t *testing.T) {
	f := newLinkFixture(t, plainScript(), nil)
	relay := f.relay.Relay()
	started := f.start(t)
	loginID := started.Login.LoginID
	for _, wantLeft := range []int{4, 3} {
		snap, err := relay.SubmitCode(context.Background(), loginID, "00000")
		if !errors.Is(err, identity.ErrCodeInvalid) {
			t.Fatalf("wrong code error = %v, want ErrCodeInvalid", err)
		}
		failure := identity.FailureOf(err)
		if failure.Code != identity.CodeCodeInvalid || failure.AttemptsLeft != wantLeft {
			t.Fatalf("failure = %+v, want code_invalid with %d attempts left", failure, wantLeft)
		}
		if snap.State != identity.StateAwaitingCode || snap.Login == nil || snap.Login.LoginID != loginID || snap.Login.CodeAttemptsLeft != wantLeft {
			t.Fatalf("snapshot after wrong code = %+v", snap)
		}
	}
	snap, err := relay.SubmitCode(context.Background(), loginID, linkCode)
	if err != nil {
		t.Fatalf("correct code after retries: %v", err)
	}
	if snap.State != identity.StateLinked || snap.Account == nil || snap.Account.UserID != linkUserID {
		t.Fatalf("snapshot after correct code = %+v", snap)
	}
}

func TestPasswordInvalidRetryInPlace(t *testing.T) {
	f := newLinkFixture(t, passwordScript(), nil)
	relay := f.relay.Relay()
	awaiting := f.toPasswordStage(t)
	loginID := awaiting.Login.LoginID
	if awaiting.Login.PasswordHint != linkPasswordTip {
		t.Fatalf("password hint = %q", awaiting.Login.PasswordHint)
	}
	for _, wantLeft := range []int{4, 3} {
		snap, err := relay.SubmitPassword(context.Background(), loginID, "wrong password")
		if !errors.Is(err, identity.ErrPasswordInvalid) {
			t.Fatalf("wrong password error = %v, want ErrPasswordInvalid", err)
		}
		if got := identity.FailureOf(err); got.Code != identity.CodePasswordInvalid || got.AttemptsLeft != wantLeft {
			t.Fatalf("failure = %+v, want password_invalid with %d attempts left", got, wantLeft)
		}
		if snap.State != identity.StateAwaitingPassword || snap.Login == nil || snap.Login.PasswordAttemptsLeft != wantLeft {
			t.Fatalf("snapshot after wrong password = %+v", snap)
		}
	}
	snap, err := relay.SubmitPassword(context.Background(), loginID, linkPassword)
	if err != nil {
		t.Fatalf("correct password after retries: %v", err)
	}
	if snap.State != identity.StateLinked {
		t.Fatalf("state = %s, want linked", snap.State)
	}
}

func TestAttemptsExhausted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script faketg.LoginScript
		stage  func(t *testing.T, f *linkFixture) string
		submit func(relay LoginRelay, loginID string) (identity.Snapshot, error)
		wrong  identity.FailureCode
	}{
		{
			name:   "five invalid codes",
			script: plainScript(),
			stage:  func(t *testing.T, f *linkFixture) string { return f.start(t).Login.LoginID },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitCode(context.Background(), loginID, "00000")
			},
			wrong: identity.CodeCodeInvalid,
		},
		{
			name:   "five invalid passwords",
			script: passwordScript(),
			stage:  func(t *testing.T, f *linkFixture) string { return f.toPasswordStage(t).Login.LoginID },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitPassword(context.Background(), loginID, "wrong password")
			},
			wrong: identity.CodePasswordInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinkFixture(t, tc.script, nil)
			relay := f.relay.Relay()
			loginID := tc.stage(t, f)
			for attempt := 1; attempt <= 4; attempt++ {
				if _, err := tc.submit(relay, loginID); identity.FailureOf(err).Code != tc.wrong {
					t.Fatalf("attempt %d error = %v, want %s", attempt, err, tc.wrong)
				}
			}
			snap, err := tc.submit(relay, loginID)
			if !errors.Is(err, identity.ErrAttemptsExhausted) {
				t.Fatalf("fifth attempt error = %v, want ErrAttemptsExhausted", err)
			}
			if snap.State != identity.StateReady || snap.Login != nil {
				t.Fatalf("snapshot after exhaustion = %+v", snap)
			}
			if snap.LastError == nil || snap.LastError.Code != identity.CodeAttemptsExhausted {
				t.Fatalf("last error = %+v", snap.LastError)
			}
			if _, err := tc.submit(relay, loginID); !errors.Is(err, identity.ErrLoginNotActive) {
				t.Fatalf("submit after exhaustion error = %v, want ErrLoginNotActive", err)
			}
		})
	}
}

func TestCodeExpiredEndsAttempt(t *testing.T) {
	script := plainScript()
	script.CodeLifetime = time.Minute
	f := newLinkFixture(t, script, nil)
	relay := f.relay.Relay()
	started := f.start(t)
	f.clock.Advance(2 * time.Minute)
	snap, err := relay.SubmitCode(context.Background(), started.Login.LoginID, linkCode)
	if !errors.Is(err, identity.ErrCodeExpired) {
		t.Fatalf("expired code error = %v, want ErrCodeExpired", err)
	}
	if snap.State != identity.StateReady || snap.Login != nil {
		t.Fatalf("snapshot after expiry = %+v", snap)
	}
	if snap.LastError == nil || snap.LastError.Code != identity.CodeCodeExpired {
		t.Fatalf("last error = %+v", snap.LastError)
	}
	if _, err := relay.SubmitCode(context.Background(), started.Login.LoginID, linkCode); !errors.Is(err, identity.ErrLoginNotActive) {
		t.Fatalf("submit after expiry error = %v, want ErrLoginNotActive", err)
	}
}

func TestSubmitInWrongStageRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script faketg.LoginScript
		stage  func(t *testing.T, f *linkFixture) string
		submit func(relay LoginRelay, loginID string) (identity.Snapshot, error)
		state  identity.State
	}{
		{
			name:   "code with no attempt",
			script: plainScript(),
			stage:  func(t *testing.T, f *linkFixture) string { return "lg_unknown00000" },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitCode(context.Background(), loginID, linkCode)
			},
			state: identity.StateNoCredentials,
		},
		{
			name:   "password while awaiting code",
			script: plainScript(),
			stage:  func(t *testing.T, f *linkFixture) string { return f.start(t).Login.LoginID },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitPassword(context.Background(), loginID, linkPassword)
			},
			state: identity.StateAwaitingCode,
		},
		{
			name:   "code while awaiting password",
			script: passwordScript(),
			stage:  func(t *testing.T, f *linkFixture) string { return f.toPasswordStage(t).Login.LoginID },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitCode(context.Background(), loginID, linkCode)
			},
			state: identity.StateAwaitingPassword,
		},
		{
			name:   "code with a login id of another attempt",
			script: plainScript(),
			stage:  func(t *testing.T, f *linkFixture) string { f.start(t); return "lg_someoneelse0" },
			submit: func(relay LoginRelay, loginID string) (identity.Snapshot, error) {
				return relay.SubmitCode(context.Background(), loginID, linkCode)
			},
			state: identity.StateAwaitingCode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinkFixture(t, tc.script, nil)
			loginID := tc.stage(t, f)
			snap, err := tc.submit(f.relay.Relay(), loginID)
			if !errors.Is(err, identity.ErrLoginNotActive) {
				t.Fatalf("error = %v, want ErrLoginNotActive", err)
			}
			if got := identity.FailureOf(err).Code; got != identity.CodeLoginNotActive {
				t.Fatalf("failure code = %s, want %s", got, identity.CodeLoginNotActive)
			}
			if snap.State != tc.state {
				t.Fatalf("state after rejected submit = %s, want %s", snap.State, tc.state)
			}
		})
	}
}

func TestAttemptTimeout(t *testing.T) {
	f := newLinkFixture(t, plainScript(), nil)
	relay := f.relay.Relay()
	started := f.start(t)
	f.clock.Advance(14*time.Minute + 59*time.Second)
	snap, err := relay.Status(context.Background())
	if err != nil || snap.State != identity.StateAwaitingCode {
		t.Fatalf("status just before the deadline = %+v, %v", snap, err)
	}
	f.clock.Advance(2 * time.Second)
	snap, err = relay.Status(context.Background())
	if err != nil || snap.State != identity.StateReady || snap.Login != nil {
		t.Fatalf("status after the deadline = %+v, %v", snap, err)
	}
	if _, err := relay.SubmitCode(context.Background(), started.Login.LoginID, linkCode); !errors.Is(err, identity.ErrLoginNotActive) {
		t.Fatalf("submit after timeout error = %v, want ErrLoginNotActive", err)
	}
	if _, err := relay.Start(context.Background(), linkPhone); err != nil {
		t.Fatalf("a new attempt after timeout: %v", err)
	}
}

func TestAccountMismatch(t *testing.T) {
	bound := identity.StoredAccount{Role: string(identity.RolePrimary), UserID: otherUserID, Linked: false}
	f := newLinkFixture(t, plainScript(), &bound)
	started := f.start(t)
	snap, err := f.relay.Relay().SubmitCode(context.Background(), started.Login.LoginID, linkCode)
	if !errors.Is(err, identity.ErrAccountMismatch) {
		t.Fatalf("error = %v, want ErrAccountMismatch", err)
	}
	if got := identity.FailureOf(err); got.Code != identity.CodeAccountMismatch || got.BoundUserID != otherUserID {
		t.Fatalf("failure = %+v", got)
	}
	if snap.State != identity.StateReady || snap.Account != nil {
		t.Fatalf("snapshot after mismatch = %+v", snap)
	}
	if f.client.Authorized() {
		t.Fatal("rejected authorization was not logged out")
	}
	if f.sessions.count() != 1 {
		t.Fatalf("session deletes = %d, want 1", f.sessions.count())
	}
}

func TestStatusNeverContainsSecrets(t *testing.T) {
	f := newLinkFixture(t, passwordScript(), nil)
	relay := f.relay.Relay()
	ctx := context.Background()
	var transcript strings.Builder
	record := func(snap identity.Snapshot, err error) identity.Snapshot {
		transcript.WriteString(snapshotText(t, snap, err))
		transcript.WriteByte('\n')
		return snap
	}
	record(relay.Status(ctx))
	if err := relay.SaveCredentials(ctx, linkAPIID, linkAPIHash); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	record(relay.Status(ctx))
	started := record(relay.Start(ctx, linkPhone))
	loginID := started.Login.LoginID
	record(relay.SubmitCode(ctx, loginID, "00000"))
	record(relay.Status(ctx))
	record(relay.SubmitCode(ctx, loginID, linkCode))
	record(relay.SubmitPassword(ctx, loginID, "not the password"))
	record(relay.Status(ctx))
	linked := record(relay.SubmitPassword(ctx, loginID, linkPassword))
	if linked.State != identity.StateLinked {
		t.Fatalf("state = %s, want linked", linked.State)
	}
	record(relay.Status(ctx))
	record(relay.Cancel(ctx))
	record(relay.Unlink(ctx))
	record(relay.Status(ctx))
	output := transcript.String() + f.logs.String()
	for _, secret := range []string{linkAPIHash, "faketg-code-", linkPassword, "not the password", "447700900134", "session"} {
		if strings.Contains(output, secret) {
			t.Fatalf("relay output contains %q:\n%s", secret, output)
		}
	}
	if !strings.Contains(output, "+44 ••• •• 34") {
		t.Fatalf("masked phone missing from relay output:\n%s", output)
	}
}

func TestUnlinkCallsLogOutBestEffort(t *testing.T) {
	for _, tc := range []struct {
		name           string
		logOutFails    bool
		wantAuthorized bool
		wantLastError  identity.FailureCode
	}{
		{"logout succeeds", false, false, ""},
		{"logout fails and unlink continues", true, true, identity.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinkFixture(t, plainScript(), nil)
			relay := f.relay.Relay()
			started := f.start(t)
			if _, err := relay.SubmitCode(context.Background(), started.Login.LoginID, linkCode); err != nil {
				t.Fatalf("link: %v", err)
			}
			if !f.client.Authorized() {
				t.Fatal("client not authorized after link")
			}
			if tc.logOutFails {
				fault := faketg.Fault{Kind: faketg.FaultError, Err: errors.New("telegram unreachable")}
				if err := f.client.QueueFaults(faketg.MethodLogOut, fault); err != nil {
					t.Fatalf("queue fault: %v", err)
				}
			}
			snap, err := relay.Unlink(context.Background())
			if err != nil {
				t.Fatalf("unlink: %v", err)
			}
			if snap.State != identity.StateReady || snap.Account != nil {
				t.Fatalf("snapshot after unlink = %+v", snap)
			}
			if f.client.Authorized() != tc.wantAuthorized {
				t.Fatalf("client authorized = %v, want %v", f.client.Authorized(), tc.wantAuthorized)
			}
			if f.sessions.count() != 1 {
				t.Fatalf("session deletes = %d, want 1", f.sessions.count())
			}
			if tc.wantLastError == "" && snap.LastError != nil {
				t.Fatalf("last error = %+v, want none", snap.LastError)
			}
			if tc.wantLastError != "" && (snap.LastError == nil || snap.LastError.Code != tc.wantLastError) {
				t.Fatalf("last error = %+v, want %s", snap.LastError, tc.wantLastError)
			}
		})
	}
}
