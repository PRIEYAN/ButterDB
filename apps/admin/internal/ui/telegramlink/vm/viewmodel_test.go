package vm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeFlow struct {
	status    adminv1.TelegramStatus
	statusErr error
	reply     func(call string) (adminv1.TelegramStatus, error)
	calls     []string
	requests  []any
}

func (f *fakeFlow) respond(call string, request any) (adminv1.TelegramStatus, error) {
	f.calls = append(f.calls, call)
	f.requests = append(f.requests, request)
	if f.reply != nil {
		if st, err := f.reply(call); st.Next != "" || err != nil {
			return st, err
		}
	}
	return f.status, f.statusErr
}

func (f *fakeFlow) Status(context.Context) (adminv1.TelegramStatus, error) {
	f.calls = append(f.calls, "Status")
	return f.status, f.statusErr
}

func (f *fakeFlow) SaveApp(_ context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error) {
	return f.respond("SaveApp", req)
}

func (f *fakeFlow) DeleteApp(context.Context) (adminv1.TelegramStatus, error) {
	return f.respond("DeleteApp", nil)
}

func (f *fakeFlow) Start(_ context.Context, phone string) (adminv1.TelegramStatus, error) {
	return f.respond("Start", phone)
}

func (f *fakeFlow) SubmitCode(_ context.Context, loginID, code string) (adminv1.TelegramStatus, error) {
	return f.respond("SubmitCode", [2]string{loginID, code})
}

func (f *fakeFlow) SubmitPassword(_ context.Context, loginID, password string) (adminv1.TelegramStatus, error) {
	return f.respond("SubmitPassword", [2]string{loginID, password})
}

func (f *fakeFlow) Cancel(context.Context) (adminv1.TelegramStatus, error) {
	return f.respond("Cancel", nil)
}

func (f *fakeFlow) called(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func statusFor(next adminv1.TelegramNext) adminv1.TelegramStatus {
	st := adminv1.TelegramStatus{Next: next}
	switch next {
	case adminv1.NextApp:
		st.State = adminv1.TelegramNoCredentials
	case adminv1.NextPhone:
		st.State = adminv1.TelegramReady
		st.Credentials = adminv1.TelegramCredentials{APIID: 1234567, APIHashSet: true}
	case adminv1.NextWait:
		st.State = adminv1.TelegramSendingCode
		st.Login = &adminv1.TelegramLogin{LoginID: "lg_1"}
	case adminv1.NextCode:
		st.State = adminv1.TelegramAwaitingCode
		st.Login = &adminv1.TelegramLogin{LoginID: "lg_1", PhoneMasked: "+44 77•• ••• •34", CodeAttemptsLeft: 5, PasswordAttemptsLeft: 5, CodeDelivery: adminv1.CodeDelivery{Type: "app", Length: 5}}
	case adminv1.NextPassword:
		st.State = adminv1.TelegramAwaitingPassword
		st.Login = &adminv1.TelegramLogin{LoginID: "lg_1", PasswordHint: "first pet", CodeAttemptsLeft: 5, PasswordAttemptsLeft: 5}
	case adminv1.NextNone:
		st.State = adminv1.TelegramLinked
		st.Account = &adminv1.TelegramAccount{UserID: 7, FirstName: "Ada", Username: "ada"}
	}
	return st
}

func apiError(code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: 422, Body: adminv1.ErrorBody{Code: code, Details: details}}
}

func newModel(t *testing.T, flow *fakeFlow) *ViewModel {
	t.Helper()
	v := New(flow, func() time.Time { return testNow })
	v.Refresh(context.Background())
	return v
}

func TestStepFollowsNext(t *testing.T) {
	cases := []struct {
		name string
		next adminv1.TelegramNext
		want Step
	}{
		{name: "app", next: adminv1.NextApp, want: StepApp},
		{name: "phone", next: adminv1.NextPhone, want: StepPhone},
		{name: "wait", next: adminv1.NextWait, want: StepWait},
		{name: "code", next: adminv1.NextCode, want: StepCode},
		{name: "password", next: adminv1.NextPassword, want: StepPassword},
		{name: "none is done", next: adminv1.NextNone, want: StepDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newModel(t, &fakeFlow{status: statusFor(tc.next)})
			if got := v.State().Get().Step; got != tc.want {
				t.Fatalf("Step = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("a submit result moves the step", func(t *testing.T) {
		flow := &fakeFlow{status: statusFor(adminv1.NextPhone)}
		v := newModel(t, flow)
		flow.status = statusFor(adminv1.NextCode)
		v.StartLogin(context.Background(), "+447700900134")
		if got := v.State().Get().Step; got != StepCode {
			t.Fatalf("Step = %q, want code", got)
		}
	})

	t.Run("unknown next keeps the step", func(t *testing.T) {
		flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
		v := newModel(t, flow)
		flow.status = adminv1.TelegramStatus{Next: "mystery"}
		v.Refresh(context.Background())
		if got := v.State().Get().Step; got != StepCode {
			t.Fatalf("Step = %q, want code", got)
		}
	})
}

func TestCodeInvalidStaysOnStep(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	before := v.State().Get().FocusSeq
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramCodeInvalid, map[string]any{"attempts_left": float64(4)})
	}

	v.SubmitCode(context.Background(), "11111")

	s := v.State().Get()
	if s.Step != StepCode {
		t.Fatalf("Step = %q, want code", s.Step)
	}
	if s.Problem == nil || s.Problem.Presentation != model.PresentInline || s.Problem.Field != "code" {
		t.Fatalf("Problem = %+v, want inline on code", s.Problem)
	}
	if !strings.Contains(s.Problem.Title, "4 attempts left") {
		t.Fatalf("Title = %q, want attempts left", s.Problem.Title)
	}
	if s.Focus != FieldCode || s.FocusSeq == before {
		t.Fatalf("Focus = %q seq %d (was %d), want a new focus request on code", s.Focus, s.FocusSeq, before)
	}
	if s.Login == nil || s.Login.CodeAttemptsLeft != 4 {
		t.Fatalf("Login = %+v, want 4 code attempts left", s.Login)
	}
	if s.Loading {
		t.Fatalf("Loading still set")
	}
	if flow.called("Start") != 0 {
		t.Fatalf("a new code was requested")
	}
}

func TestPasswordInvalidStaysOnStep(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextPassword)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramPasswordInvalid, map[string]any{"attempts_left": 3})
	}

	v.SubmitPassword(context.Background(), "wrong")

	s := v.State().Get()
	if s.Step != StepPassword {
		t.Fatalf("Step = %q, want password", s.Step)
	}
	if s.Problem == nil || s.Problem.Field != "password" || !strings.Contains(s.Problem.Title, "3 attempts left") {
		t.Fatalf("Problem = %+v, want inline password with 3 attempts left", s.Problem)
	}
	if s.Focus != FieldPassword {
		t.Fatalf("Focus = %q, want password", s.Focus)
	}
	if s.Login == nil || s.Login.PasswordAttemptsLeft != 3 {
		t.Fatalf("Login = %+v, want 3 password attempts left", s.Login)
	}
}

func TestCodeExpiredReturnsToPhone(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		flow.status = statusFor(adminv1.NextPhone)
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramCodeExpired, nil)
	}

	v.SubmitCode(context.Background(), "11111")

	s := v.State().Get()
	if s.Step != StepPhone {
		t.Fatalf("Step = %q, want phone", s.Step)
	}
	if s.Problem == nil || s.Problem.Title != "The code expired. Request a new one." || s.Problem.Field != "phone" {
		t.Fatalf("Problem = %+v, want the expiry message on phone", s.Problem)
	}
	if s.Focus != FieldPhone {
		t.Fatalf("Focus = %q, want phone", s.Focus)
	}
}

func TestAttemptsExhaustedReturnsToPhone(t *testing.T) {
	cases := []struct {
		name       string
		statusFail bool
	}{
		{name: "server status says phone"},
		{name: "status refresh fails", statusFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flow := &fakeFlow{status: statusFor(adminv1.NextPassword)}
			v := newModel(t, flow)
			flow.reply = func(call string) (adminv1.TelegramStatus, error) {
				flow.status = statusFor(adminv1.NextPhone)
				if tc.statusFail {
					flow.statusErr = fmt.Errorf("status down")
				}
				return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramAttemptsExhausted, nil)
			}

			v.SubmitPassword(context.Background(), "wrong")

			s := v.State().Get()
			if s.Step != StepPhone {
				t.Fatalf("Step = %q, want phone", s.Step)
			}
			if s.Problem == nil || s.Problem.Title != "Too many wrong attempts. Start again." {
				t.Fatalf("Problem = %+v, want the exhausted message", s.Problem)
			}
		})
	}
}

func TestFloodWaitDisablesSubmit(t *testing.T) {
	now := testNow
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := New(flow, func() time.Time { return now })
	v.Refresh(context.Background())
	until := testNow.Add(90 * time.Second)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramFloodWait, map[string]any{"until": until.Format(time.RFC3339)})
	}

	v.SubmitCode(context.Background(), "11111")

	s := v.State().Get()
	if !s.SubmitDisabled || !s.FloodUntil.Equal(until) {
		t.Fatalf("SubmitDisabled = %v FloodUntil = %v, want disabled until %v", s.SubmitDisabled, s.FloodUntil, until)
	}
	if s.Step != StepCode || s.Problem == nil || s.Problem.Presentation != model.PresentBanner || !s.Problem.RetryAt.Equal(until) {
		t.Fatalf("Step = %q Problem = %+v, want a banner with RetryAt on the same step", s.Step, s.Problem)
	}

	submits := flow.called("SubmitCode")
	v.SubmitCode(context.Background(), "22222")
	if flow.called("SubmitCode") != submits {
		t.Fatalf("a submit went out during the flood wait")
	}

	now = until.Add(-time.Second)
	v.Tick()
	if !v.State().Get().SubmitDisabled {
		t.Fatalf("submit re-enabled before the wait ended")
	}
	now = until
	v.Tick()
	if v.State().Get().SubmitDisabled {
		t.Fatalf("submit still disabled after the wait ended")
	}
}

func TestLoginInProgressResumes(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextPhone)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		flow.status = statusFor(adminv1.NextCode)
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramLoginInProgress, nil)
	}

	v.StartLogin(context.Background(), "+447700900134")

	s := v.State().Get()
	if s.Step != StepCode {
		t.Fatalf("Step = %q, want code", s.Step)
	}
	if s.Login == nil || s.Login.LoginID != "lg_1" {
		t.Fatalf("Login = %+v, want the resumed attempt", s.Login)
	}
	if s.Problem == nil || s.Problem.Code != adminv1.CodeTelegramLoginInProgress {
		t.Fatalf("Problem = %+v, want the resume notice", s.Problem)
	}
}

func TestLoginNotActiveRefreshes(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		flow.status = statusFor(adminv1.NextPhone)
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramLoginNotActive, nil)
	}

	v.SubmitCode(context.Background(), "11111")

	if got := v.State().Get().Step; got != StepPhone {
		t.Fatalf("Step = %q, want phone", got)
	}
}

func TestSecretsClearedAfterSubmit(t *testing.T) {
	const (
		hash     = "0123456789abcdef0123456789abcdef"
		code     = "48151"
		password = "correct horse battery"
	)
	flow := &fakeFlow{status: statusFor(adminv1.NextApp)}
	v := newModel(t, flow)

	flow.status = statusFor(adminv1.NextPhone)
	v.SaveApp(context.Background(), "1234567", strings.ToUpper(hash))
	flow.status = statusFor(adminv1.NextCode)
	v.StartLogin(context.Background(), "+44 7700 900134")
	flow.status = statusFor(adminv1.NextPassword)
	v.SubmitCode(context.Background(), code)
	flow.status = statusFor(adminv1.NextNone)
	v.SubmitPassword(context.Background(), password)

	if got := flow.requests[0].(adminv1.TelegramCredentialsRequest); got.APIHash != hash || got.APIID != 1234567 {
		t.Fatalf("SaveApp request = %+v, want the lowercased hash", got)
	}
	if got := flow.requests[2].([2]string); got != [2]string{"lg_1", code} {
		t.Fatalf("SubmitCode request = %v", got)
	}
	if got := flow.requests[3].([2]string); got != [2]string{"lg_1", password} {
		t.Fatalf("SubmitPassword request = %v", got)
	}
	retained := fmt.Sprintf("%+v %+v %+v", v.State().Get(), v.override, v.lastNext)
	for _, secret := range []string{hash, strings.ToUpper(hash), code, password} {
		if strings.Contains(retained, secret) {
			t.Fatalf("view model retains a secret after submit")
		}
	}
}

func TestCancelReturnsToPhone(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return statusFor(adminv1.NextPhone), nil
	}

	v.Cancel(context.Background())

	s := v.State().Get()
	if s.Step != StepPhone || s.Login != nil {
		t.Fatalf("Step = %q Login = %+v, want phone and no login", s.Step, s.Login)
	}
	if flow.called("Cancel") != 1 {
		t.Fatalf("Cancel called %d times", flow.called("Cancel"))
	}
}

func TestInputPrechecks(t *testing.T) {
	cases := []struct {
		name      string
		run       func(v *ViewModel)
		wantField string
		wantCall  string
	}{
		{name: "api id letters", run: func(v *ViewModel) { v.SaveApp(context.Background(), "12ab", strings.Repeat("a", 32)) }, wantField: "api_id"},
		{name: "api id zero", run: func(v *ViewModel) { v.SaveApp(context.Background(), "0", strings.Repeat("a", 32)) }, wantField: "api_id"},
		{name: "api id too large", run: func(v *ViewModel) { v.SaveApp(context.Background(), "2147483648", strings.Repeat("a", 32)) }, wantField: "api_id"},
		{name: "api hash short", run: func(v *ViewModel) { v.SaveApp(context.Background(), "1", "abc") }, wantField: "api_hash"},
		{name: "api hash not hex", run: func(v *ViewModel) { v.SaveApp(context.Background(), "1", strings.Repeat("z", 32)) }, wantField: "api_hash"},
		{name: "phone letters", run: func(v *ViewModel) { v.StartLogin(context.Background(), "+44abc") }, wantField: "phone"},
		{name: "phone too short", run: func(v *ViewModel) { v.StartLogin(context.Background(), "+4412") }, wantField: "phone"},
		{name: "phone leading zero", run: func(v *ViewModel) { v.StartLogin(context.Background(), "0770090013") }, wantField: "phone"},
		{name: "empty code", run: func(v *ViewModel) { v.SubmitCode(context.Background(), "  ") }, wantField: "code"},
		{name: "empty password", run: func(v *ViewModel) { v.SubmitPassword(context.Background(), "") }, wantField: "password"},
		{name: "phone gets plus prefix", run: func(v *ViewModel) { v.StartLogin(context.Background(), "(44) 7700-900134") }, wantCall: "Start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
			v := newModel(t, flow)
			calls := len(flow.calls)

			tc.run(v)

			s := v.State().Get()
			if tc.wantField != "" {
				if len(flow.calls) != calls {
					t.Fatalf("a request went out for invalid input: %v", flow.calls[calls:])
				}
				if s.Problem == nil || s.Problem.Field != tc.wantField || s.Problem.Presentation != model.PresentInline {
					t.Fatalf("Problem = %+v, want inline on %s", s.Problem, tc.wantField)
				}
				if string(s.Focus) != tc.wantField {
					t.Fatalf("Focus = %q, want %q", s.Focus, tc.wantField)
				}
				return
			}
			if flow.called(tc.wantCall) != 1 {
				t.Fatalf("calls = %v, want %s", flow.calls, tc.wantCall)
			}
			if got := flow.requests[len(flow.requests)-1]; got != "+447700900134" {
				t.Fatalf("phone sent = %v, want +447700900134", got)
			}
		})
	}
}

func TestAPIIDInvalidGoesToAppStep(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextPhone)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramAPIIDInvalid, nil)
	}

	v.StartLogin(context.Background(), "+447700900134")
	s := v.State().Get()
	if s.Step != StepApp || s.Problem == nil || s.Problem.Title != "Telegram rejected this api_id and api_hash." {
		t.Fatalf("Step = %q Problem = %+v, want app step with the rejection", s.Step, s.Problem)
	}

	v.Poll(context.Background())
	if got := v.State().Get().Step; got != StepApp {
		t.Fatalf("poll moved the step to %q while the server still says phone", got)
	}

	flow.reply = nil
	flow.status = statusFor(adminv1.NextPhone)
	flow.status.Credentials.APIID = 7654321
	v.SaveApp(context.Background(), "7654321", strings.Repeat("a", 32))
	if got := v.State().Get().Step; got != StepPhone {
		t.Fatalf("Step = %q after saving new credentials, want phone", got)
	}
}

func TestDialogReactionsStayOrLeave(t *testing.T) {
	cases := []struct {
		name     string
		code     adminv1.Code
		start    adminv1.TelegramNext
		after    adminv1.TelegramNext
		wantStep Step
		wantPres model.Presentation
	}{
		{name: "email setup stays on phone", code: adminv1.CodeTelegramEmailSetupRequired, start: adminv1.NextPhone, after: adminv1.NextPhone, wantStep: StepPhone, wantPres: model.PresentDialog},
		{name: "account mismatch goes to phone", code: adminv1.CodeTelegramAccountMismatch, start: adminv1.NextCode, after: adminv1.NextPhone, wantStep: StepPhone, wantPres: model.PresentDialog},
		{name: "login failed goes to phone", code: adminv1.CodeTelegramLoginFailed, start: adminv1.NextCode, after: adminv1.NextPhone, wantStep: StepPhone, wantPres: model.PresentDialog},
		{name: "unavailable keeps the step", code: adminv1.CodeTelegramUnavailable, start: adminv1.NextCode, after: adminv1.NextCode, wantStep: StepCode, wantPres: model.PresentBanner},
		{name: "phone invalid stays inline", code: adminv1.CodeTelegramPhoneInvalid, start: adminv1.NextPhone, after: adminv1.NextPhone, wantStep: StepPhone, wantPres: model.PresentInline},
		{name: "unauthenticated routes away", code: adminv1.CodeUnauthenticated, start: adminv1.NextCode, after: adminv1.NextCode, wantStep: StepCode, wantPres: model.PresentRoute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flow := &fakeFlow{status: statusFor(tc.start)}
			v := newModel(t, flow)
			flow.reply = func(call string) (adminv1.TelegramStatus, error) {
				flow.status = statusFor(tc.after)
				return adminv1.TelegramStatus{}, apiError(tc.code, nil)
			}

			v.SubmitCode(context.Background(), "12345")
			if tc.start == adminv1.NextPhone {
				v.StartLogin(context.Background(), "+447700900134")
			}

			s := v.State().Get()
			if s.Step != tc.wantStep {
				t.Fatalf("Step = %q, want %q", s.Step, tc.wantStep)
			}
			if s.Problem == nil || s.Problem.Presentation != tc.wantPres {
				t.Fatalf("Problem = %+v, want presentation %q", s.Problem, tc.wantPres)
			}
		})
	}
}

func TestCanceledContextRaisesNothing(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, context.Canceled
	}

	v.SubmitCode(context.Background(), "12345")

	s := v.State().Get()
	if s.Problem != nil || s.Loading {
		t.Fatalf("Problem = %+v Loading = %v, want neither", s.Problem, s.Loading)
	}
}

func TestPollKeepsInlineProblemOnSameStep(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextCode)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return adminv1.TelegramStatus{}, apiError(adminv1.CodeTelegramCodeInvalid, nil)
	}
	v.SubmitCode(context.Background(), "12345")

	v.Poll(context.Background())

	if v.State().Get().Problem == nil {
		t.Fatalf("poll dropped the inline problem on an unchanged step")
	}
}

func TestPollFollowsAnotherDevice(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextPhone)}
	v := newModel(t, flow)
	flow.status = statusFor(adminv1.NextPassword)

	v.Poll(context.Background())

	s := v.State().Get()
	if s.Step != StepPassword || s.Login == nil || s.Login.PasswordHint != "first pet" {
		t.Fatalf("Step = %q Login = %+v, want the password step from the other device", s.Step, s.Login)
	}
}

func TestIntervalByStep(t *testing.T) {
	cases := []struct {
		name string
		next adminv1.TelegramNext
		want time.Duration
	}{
		{name: "wait polls fast", next: adminv1.NextWait, want: 2 * time.Second},
		{name: "code polls slowly", next: adminv1.NextCode, want: 10 * time.Second},
		{name: "phone polls slowly", next: adminv1.NextPhone, want: 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newModel(t, &fakeFlow{status: statusFor(tc.next)})
			if got := v.Interval(); got != tc.want {
				t.Fatalf("Interval = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChangeAppReturnsToAppStep(t *testing.T) {
	flow := &fakeFlow{status: statusFor(adminv1.NextPhone)}
	v := newModel(t, flow)
	flow.reply = func(call string) (adminv1.TelegramStatus, error) {
		return statusFor(adminv1.NextApp), nil
	}

	v.ChangeApp(context.Background())

	if got := v.State().Get().Step; got != StepApp {
		t.Fatalf("Step = %q, want app", got)
	}
}
