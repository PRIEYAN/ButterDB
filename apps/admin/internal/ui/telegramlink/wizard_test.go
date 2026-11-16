package telegramlink

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeFlow struct {
	mu       sync.Mutex
	status   adminv1.TelegramStatus
	failWith error
	codes    []string
	passes   []string
	hashes   []string
	started  []string
	cancels  int
	deleted  int
}

func (f *fakeFlow) setStatus(st adminv1.TelegramStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = st
}

func (f *fakeFlow) setFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}

func (f *fakeFlow) result() (adminv1.TelegramStatus, error) {
	if f.failWith != nil {
		return adminv1.TelegramStatus{}, f.failWith
	}
	return f.status, nil
}

func (f *fakeFlow) Status(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeFlow) SaveApp(_ context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes = append(f.hashes, req.APIHash)
	return f.result()
}

func (f *fakeFlow) DeleteApp(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted++
	return f.result()
}

func (f *fakeFlow) Start(_ context.Context, phone string) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, phone)
	return f.result()
}

func (f *fakeFlow) SubmitCode(_ context.Context, _, code string) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes = append(f.codes, code)
	return f.result()
}

func (f *fakeFlow) SubmitPassword(_ context.Context, _, password string) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes = append(f.passes, password)
	return f.result()
}

func (f *fakeFlow) Cancel(context.Context) (adminv1.TelegramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return f.result()
}

type recordedNotifier struct {
	problems []model.Problem
}

func (n *recordedNotifier) Problem(p model.Problem) { n.problems = append(n.problems, p) }
func (n *recordedNotifier) Info(string)             {}
func (n *recordedNotifier) ClearBanner()            {}

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
		st.Login = &adminv1.TelegramLogin{
			LoginID:          "lg_1",
			PhoneMasked:      "+44 77•• ••• •34",
			CodeAttemptsLeft: 5,
			ExpiresAt:        testNow.Add(14*time.Minute + 32*time.Second),
			CodeDelivery:     adminv1.CodeDelivery{Type: "app", Length: 5},
		}
	case adminv1.NextPassword:
		st.State = adminv1.TelegramAwaitingPassword
		st.Login = &adminv1.TelegramLogin{LoginID: "lg_1", PasswordHint: "first pet", PasswordAttemptsLeft: 4}
	case adminv1.NextNone:
		st.State = adminv1.TelegramLinked
		st.Account = &adminv1.TelegramAccount{UserID: 7, FirstName: "Ada", Username: "ada", PhoneMasked: "+44 77•• ••• •34"}
	}
	return st
}

func apiError(code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: 422, Body: adminv1.ErrorBody{Code: code, Details: details}}
}

type harness struct {
	t      *testing.T
	flow   *fakeFlow
	notify *recordedNotifier
	wizard *LoginWizard
	win    fyne.Window
}

func newHarness(t *testing.T, next adminv1.TelegramNext, opts WizardOptions) *harness {
	t.Helper()
	test.NewTempApp(t)
	flow := &fakeFlow{status: statusFor(next)}
	notify := &recordedNotifier{}
	v := vm.New(flow, func() time.Time { return testNow })
	wizard := NewLoginWizardFromViewModel(nil, v, opts, notify)
	win := test.NewTempWindow(t, wizard.Content())
	wizard.win = win
	h := &harness{t: t, flow: flow, notify: notify, wizard: wizard, win: win}
	wizard.dispatch(v.Refresh)
	h.settle()
	return h
}

func (h *harness) settle() {
	h.t.Helper()
	h.wizard.commands.Wait()
}

func (h *harness) tap(button *widget.Button) {
	h.t.Helper()
	test.Tap(button)
	h.settle()
}

func fullOptions() WizardOptions {
	return WizardOptions{AskAppCredentials: true, AllowReuseApp: true}
}

func hasText(t *testing.T, root fyne.CanvasObject, text string) bool {
	t.Helper()
	found := false
	walkVisible(t, root, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && l.Text == text {
			found = true
		}
	})
	return found
}

func walkVisible(t *testing.T, o fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	t.Helper()
	if !o.Visible() {
		return
	}
	visit(o)
	switch c := o.(type) {
	case *fyne.Container:
		for _, child := range c.Objects {
			walkVisible(t, child, visit)
		}
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, c).Objects() {
			walkVisible(t, child, visit)
		}
	}
}

func visiblePanels(w *LoginWizard) []vm.Step {
	var shown []vm.Step
	for step, panel := range w.panels {
		if panel.Visible() {
			shown = append(shown, step)
		}
	}
	return shown
}

func TestWizardRendersEachStep(t *testing.T) {
	cases := []struct {
		name      string
		next      adminv1.TelegramNext
		want      vm.Step
		wantTexts []string
	}{
		{name: "app", next: adminv1.NextApp, want: vm.StepApp, wantTexts: []string{"Step 1 of 4 · App credentials", safetyNotice, appHelp}},
		{name: "phone", next: adminv1.NextPhone, want: vm.StepPhone, wantTexts: []string{"Step 2 of 4 · Phone number"}},
		{name: "wait", next: adminv1.NextWait, want: vm.StepWait, wantTexts: []string{"Sending code", waitText}},
		{name: "code", next: adminv1.NextCode, want: vm.StepCode, wantTexts: []string{"Step 3 of 4 · Login code", "Sent to your Telegram app on another device", "+44 77•• ••• •34", "Attempts left: 5", "Code expires in 14:32"}},
		{name: "password", next: adminv1.NextPassword, want: vm.StepPassword, wantTexts: []string{"Step 4 of 4 · Two-step password", "Attempts left: 4"}},
		{name: "done", next: adminv1.NextNone, want: vm.StepDone, wantTexts: []string{"Linked", "Ada", "@ada", "+44 77•• ••• •34"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.next, fullOptions())

			shown := visiblePanels(h.wizard)
			if len(shown) != 1 || shown[0] != tc.want {
				t.Fatalf("visible panels = %v, want only %q", shown, tc.want)
			}
			for _, text := range tc.wantTexts {
				if !hasText(t, h.wizard.Content(), text) {
					t.Errorf("step %q does not show %q", tc.want, text)
				}
			}
			if cancelShown := h.wizard.cancel.Visible(); cancelShown != (tc.want != vm.StepDone) {
				t.Errorf("Cancel login visible = %v on step %q", cancelShown, tc.want)
			}
		})
	}
}

func TestWizardFollowsServerNextWhenItChanges(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())
	h.flow.setStatus(statusFor(adminv1.NextCode))

	test.Type(h.wizard.phone.entry, "+447700900134")
	h.tap(h.wizard.phone.submit)

	if shown := visiblePanels(h.wizard); len(shown) != 1 || shown[0] != vm.StepCode {
		t.Fatalf("visible panels = %v, want code", shown)
	}
	if got := h.flow.started; len(got) != 1 || got[0] != "+447700900134" {
		t.Fatalf("Start phones = %v", got)
	}
}

func TestCodeEntryFocusedAfterInvalid(t *testing.T) {
	h := newHarness(t, adminv1.NextCode, fullOptions())
	h.flow.setFailure(apiError(adminv1.CodeTelegramCodeInvalid, map[string]any{"attempts_left": float64(3)}))

	h.wizard.code.entry.SetText("11111")
	h.tap(h.wizard.code.submit)

	if got := h.wizard.code.entry.Text; got != "" {
		t.Fatalf("code entry = %q, want cleared", got)
	}
	if h.win.Canvas().Focused() != fyne.Focusable(h.wizard.code.entry) {
		t.Fatalf("focused = %v, want the code entry", h.win.Canvas().Focused())
	}
	label := h.wizard.inline[vm.FieldCode]
	if !label.Visible() || label.Text != "That code is not right. 3 attempts left." {
		t.Fatalf("inline error = %q visible %v", label.Text, label.Visible())
	}
	if shown := visiblePanels(h.wizard); len(shown) != 1 || shown[0] != vm.StepCode {
		t.Fatalf("visible panels = %v, want code", shown)
	}
	if len(h.notify.problems) != 0 {
		t.Fatalf("inline error also went to the notifier: %+v", h.notify.problems)
	}
}

func TestHintShown(t *testing.T) {
	cases := []struct {
		name     string
		hint     string
		wantText string
		visible  bool
	}{
		{name: "hint present", hint: "first pet", wantText: "Hint: first pet", visible: true},
		{name: "hint empty", hint: "", visible: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test.NewTempApp(t)
			flow := &fakeFlow{status: statusFor(adminv1.NextPassword)}
			flow.status.Login.PasswordHint = tc.hint
			v := vm.New(flow, func() time.Time { return testNow })
			wizard := NewLoginWizardFromViewModel(nil, v, fullOptions(), &recordedNotifier{})
			test.NewTempWindow(t, wizard.Content())
			v.Refresh(context.Background())

			if got := wizard.password.hint.Visible(); got != tc.visible {
				t.Fatalf("hint visible = %v, want %v", got, tc.visible)
			}
			if tc.visible && !hasText(t, wizard.Content(), tc.wantText) {
				t.Fatalf("hint %q not shown", tc.wantText)
			}
		})
	}
}

func TestSubmitClearsSecretEntries(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef"
	h := newHarness(t, adminv1.NextApp, fullOptions())
	h.flow.setStatus(statusFor(adminv1.NextPhone))

	test.Type(h.wizard.app.id, "1234567")
	h.wizard.app.hash.SetText(hash)
	h.tap(h.wizard.app.submit)
	if h.wizard.app.hash.Text != "" {
		t.Fatalf("api_hash entry not cleared")
	}
	if got := h.flow.hashes; len(got) != 1 || got[0] != hash {
		t.Fatalf("hashes sent = %v", got)
	}

	h.flow.setStatus(statusFor(adminv1.NextPassword))
	h.wizard.dispatch(h.wizard.v.Refresh)
	h.settle()
	h.wizard.password.entry.SetText("hunter2")
	h.tap(h.wizard.password.submit)
	if h.wizard.password.entry.Text != "" {
		t.Fatalf("password entry not cleared")
	}
	if got := h.flow.passes; len(got) != 1 || got[0] != "hunter2" {
		t.Fatalf("passwords sent = %v", got)
	}
}

func TestStepChangeClearsSecretEntries(t *testing.T) {
	h := newHarness(t, adminv1.NextCode, fullOptions())
	h.wizard.code.entry.SetText("123")
	h.wizard.ClearSecrets()
	if h.wizard.code.entry.Text != "" {
		t.Fatalf("code entry not cleared by ClearSecrets")
	}

	h.wizard.password.entry.SetText("left behind")
	h.flow.setStatus(statusFor(adminv1.NextPhone))
	h.wizard.dispatch(h.wizard.v.Refresh)
	h.settle()
	if h.wizard.password.entry.Text != "" {
		t.Fatalf("password entry survived a step change")
	}
}

func TestFloodWaitDisablesSubmitButton(t *testing.T) {
	h := newHarness(t, adminv1.NextCode, fullOptions())
	until := testNow.Add(time.Minute)
	h.flow.setFailure(apiError(adminv1.CodeTelegramFloodWait, map[string]any{"until": until.Format(time.RFC3339)}))

	h.wizard.code.entry.SetText("11111")
	h.tap(h.wizard.code.submit)

	if !h.wizard.code.submit.Disabled() {
		t.Fatalf("submit still enabled during the flood wait")
	}
	if len(h.notify.problems) != 1 || h.notify.problems[0].Presentation != model.PresentBanner || !h.notify.problems[0].RetryAt.Equal(until) {
		t.Fatalf("notifier problems = %+v, want one banner with RetryAt", h.notify.problems)
	}
}

func TestProblemsGoToNotifierOnce(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())
	h.flow.setFailure(apiError(adminv1.CodeTelegramEmailSetupRequired, nil))

	test.Type(h.wizard.phone.entry, "+447700900134")
	h.tap(h.wizard.phone.submit)
	h.wizard.v.Tick()
	h.wizard.refreshCountdown()

	if len(h.notify.problems) != 1 || h.notify.problems[0].Code != adminv1.CodeTelegramEmailSetupRequired {
		t.Fatalf("notifier problems = %+v, want one email setup dialog", h.notify.problems)
	}
}

func TestPhoneEntryEnforcesPlusPrefix(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())

	h.wizard.phone.entry.SetText("447700900134")

	if got := h.wizard.phone.entry.Text; got != "+447700900134" {
		t.Fatalf("phone entry = %q, want a plus prefix", got)
	}
}

func TestAPIIDEntryAcceptsDigitsOnly(t *testing.T) {
	h := newHarness(t, adminv1.NextApp, fullOptions())

	h.wizard.app.id.SetText("12ab34")

	if got := h.wizard.app.id.Text; got != "1234" {
		t.Fatalf("api_id entry = %q, want digits only", got)
	}
}

func TestCodeEntryTruncatesToDeliveryLength(t *testing.T) {
	h := newHarness(t, adminv1.NextCode, fullOptions())

	h.wizard.code.entry.SetText("1234567")

	if got := h.wizard.code.entry.Text; got != "12345" {
		t.Fatalf("code entry = %q, want 5 characters", got)
	}
}

func TestOptionsShapeTheWizard(t *testing.T) {
	cases := []struct {
		name          string
		opts          WizardOptions
		next          adminv1.TelegramNext
		wantStep      vm.Step
		wantChange    bool
		wantPhoneNote bool
	}{
		{name: "primary phone offers app change", opts: fullOptions(), next: adminv1.NextPhone, wantStep: vm.StepPhone, wantChange: true},
		{name: "reuse not allowed hides app change", opts: WizardOptions{AskAppCredentials: true}, next: adminv1.NextPhone, wantStep: vm.StepPhone},
		{name: "replica never asks for app", opts: WizardOptions{}, next: adminv1.NextApp, wantStep: vm.StepPhone, wantPhoneNote: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.next, tc.opts)

			if shown := visiblePanels(h.wizard); len(shown) != 1 || shown[0] != tc.wantStep {
				t.Fatalf("visible panels = %v, want %q", shown, tc.wantStep)
			}
			if got := h.wizard.phone.change.Visible(); got != tc.wantChange {
				t.Errorf("change app visible = %v, want %v", got, tc.wantChange)
			}
			if got := hasText(t, h.wizard.Content(), safetyNotice); got != tc.wantPhoneNote {
				t.Errorf("safety notice on phone step = %v, want %v", got, tc.wantPhoneNote)
			}
		})
	}
}

func TestChangeAppAsksBeforeDeleting(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())

	test.Tap(h.wizard.phone.change)
	h.settle()
	if h.flow.deleted != 0 {
		t.Fatalf("credentials deleted without confirmation")
	}
	if h.win.Canvas().Overlays().Top() == nil {
		t.Fatalf("no confirmation dialog shown")
	}
}

func TestCancelButtonCancelsTheLogin(t *testing.T) {
	h := newHarness(t, adminv1.NextCode, fullOptions())
	h.flow.setStatus(statusFor(adminv1.NextPhone))

	h.tap(h.wizard.cancel)

	if h.flow.cancels != 1 {
		t.Fatalf("Cancel called %d times", h.flow.cancels)
	}
	if shown := visiblePanels(h.wizard); len(shown) != 1 || shown[0] != vm.StepPhone {
		t.Fatalf("visible panels = %v, want phone", shown)
	}
}

func TestContinueReportsLinkedAccount(t *testing.T) {
	h := newHarness(t, adminv1.NextNone, fullOptions())
	var got []adminv1.TelegramAccount
	h.wizard.OnLinked(func(a adminv1.TelegramAccount) { got = append(got, a) })

	test.Tap(h.wizard.done.next)

	if len(got) != 1 || got[0].UserID != 7 {
		t.Fatalf("OnLinked accounts = %+v, want the linked account once", got)
	}
}

func TestInlinePhoneProblemShowsOnPhoneStep(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())

	h.wizard.phone.entry.SetText("+44ab")
	test.Tap(h.wizard.phone.submit)
	h.settle()

	label := h.wizard.inline[vm.FieldPhone]
	if !label.Visible() || !strings.Contains(label.Text, "Check this value") {
		t.Fatalf("phone inline error = %q visible %v", label.Text, label.Visible())
	}
	if h.win.Canvas().Focused() != fyne.Focusable(h.wizard.phone.entry) {
		t.Fatalf("focus not on the phone entry")
	}
	if len(h.flow.started) != 0 {
		t.Fatalf("Start called for an invalid number")
	}
}

func TestStartPollsAndStopsWithContext(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())
	ctx, cancel := context.WithCancel(context.Background())
	h.wizard.Start(ctx)
	h.settle()
	h.flow.setStatus(statusFor(adminv1.NextPassword))
	h.wizard.dispatch(h.wizard.v.Poll)
	h.settle()
	cancel()
	h.wizard.Wait()

	if shown := visiblePanels(h.wizard); len(shown) != 1 || shown[0] != vm.StepPassword {
		t.Fatalf("visible panels = %v, want password after a poll", shown)
	}
}

func TestScreenContract(t *testing.T) {
	h := newHarness(t, adminv1.NextPhone, fullOptions())
	screen := NewScreen(h.wizard)

	if screen.Route() != model.RouteTelegram || screen.Title() != "Link Telegram" || screen.Icon() == nil || screen.Shortcut() != "" {
		t.Fatalf("screen identity wrong: %q %q", screen.Route(), screen.Title())
	}
	if screen.Content() != h.wizard.Content() {
		t.Fatalf("screen content is not the wizard content")
	}

	h.wizard.code.entry.SetText("123")
	screen.Hide()
	if h.wizard.code.entry.Text != "" {
		t.Fatalf("Hide left a secret entry filled")
	}

	ctx, cancel := context.WithCancel(context.Background())
	screen.Show(ctx, nil)
	cancel()
	h.wizard.Wait()
}
