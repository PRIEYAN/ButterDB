package telegramlink

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

func (w *LoginWizard) render(st vm.State) {
	step := w.visibleStep(st.Step)
	if step != w.shownStep {
		w.enter(step)
	}
	w.fill(step, st)
	w.applyEnabled(st)
	w.showProblem(st)
	w.applyFocus(st)
}

func (w *LoginWizard) visibleStep(step vm.Step) vm.Step {
	if step == vm.StepApp && !w.opts.AskAppCredentials {
		return vm.StepPhone
	}
	return step
}

func (w *LoginWizard) enter(step vm.Step) {
	for s, panel := range w.panels {
		if s == step {
			panel.Show()
		} else {
			panel.Hide()
		}
	}
	w.ClearSecrets()
	w.shownStep = step
	w.stepName.SetText(stepTitle(step))
	if step == vm.StepDone {
		w.cancel.Hide()
	} else {
		w.cancel.Show()
	}
}

func (w *LoginWizard) fill(step vm.Step, st vm.State) {
	switch step {
	case vm.StepPhone:
		w.fillPhone(st)
	case vm.StepCode:
		w.fillCode(st)
	case vm.StepPassword:
		w.fillPassword(st)
	case vm.StepDone:
		w.fillDone(st)
	}
}

func (w *LoginWizard) fillPhone(st vm.State) {
	if w.opts.AskAppCredentials && w.opts.AllowReuseApp && st.Credentials.APIHashSet {
		w.phone.change.Show()
		return
	}
	w.phone.change.Hide()
}

func (w *LoginWizard) fillCode(st vm.State) {
	login := st.Login
	if login == nil {
		return
	}
	p := &w.code
	p.length = login.CodeDelivery.Length
	p.entry.SetPlaceHolder(strings.Repeat("0", max(p.length, 0)))
	p.delivery.SetText(codeDeliveryText(login.CodeDelivery.Type))
	p.phone.SetText(login.PhoneMasked)
	p.attempts.SetText(attemptsText(login.CodeAttemptsLeft))
	w.codeExpires = login.ExpiresAt
	w.refreshCountdown()
}

func (w *LoginWizard) fillPassword(st vm.State) {
	login := st.Login
	if login == nil {
		return
	}
	p := &w.password
	if login.PasswordHint != "" {
		p.hint.SetText("Hint: " + login.PasswordHint)
		p.hint.Show()
	} else {
		p.hint.SetText("")
		p.hint.Hide()
	}
	p.attempts.SetText(attemptsText(login.PasswordAttemptsLeft))
}

func (w *LoginWizard) fillDone(st vm.State) {
	account := st.Account
	if account == nil {
		return
	}
	w.done.name.SetText(account.FirstName)
	w.done.username.SetText(usernameText(account))
	w.done.phone.SetText(account.PhoneMasked)
}

func (w *LoginWizard) refreshCountdown() {
	if w.shownStep != vm.StepCode || w.codeExpires.IsZero() {
		return
	}
	w.code.expires.SetText(expiryText(w.codeExpires.Sub(w.v.Now())))
}

func (w *LoginWizard) applyEnabled(st vm.State) {
	busy := st.Loading || st.SubmitDisabled
	for _, button := range []*widget.Button{w.app.submit, w.phone.submit, w.code.submit, w.password.submit} {
		if busy {
			button.Disable()
		} else {
			button.Enable()
		}
	}
	if st.Loading {
		w.progress.Show()
	} else {
		w.progress.Hide()
	}
}

func (w *LoginWizard) showProblem(st vm.State) {
	for _, label := range w.inline {
		label.Hide()
	}
	p := st.Problem
	if p == nil {
		return
	}
	if p.Presentation == model.PresentInline {
		if label, ok := w.inline[vm.Field(p.Field)]; ok {
			label.SetText(problemText(*p))
			label.Show()
			w.seenProblem = st.ProblemSeq
			return
		}
	}
	if st.ProblemSeq == w.seenProblem {
		return
	}
	w.seenProblem = st.ProblemSeq
	if w.n != nil {
		w.n.Problem(*p)
	}
}

func (w *LoginWizard) applyFocus(st vm.State) {
	if st.FocusSeq == w.seenFocus {
		return
	}
	w.seenFocus = st.FocusSeq
	entry := w.entryFor(st.Focus)
	if entry == nil {
		return
	}
	if st.Focus == vm.FieldCode || st.Focus == vm.FieldPassword || st.Focus == vm.FieldAPIHash {
		entry.SetText("")
	}
	if w.win != nil {
		w.win.Canvas().Focus(entry)
	}
}

func (w *LoginWizard) entryFor(field vm.Field) *widget.Entry {
	switch field {
	case vm.FieldAPIID:
		return w.app.id
	case vm.FieldAPIHash:
		return w.app.hash
	case vm.FieldPhone:
		return w.phone.entry
	case vm.FieldCode:
		return w.code.entry
	case vm.FieldPassword:
		return w.password.entry
	}
	return nil
}

func problemText(p model.Problem) string {
	if p.Hint == "" {
		return p.Title
	}
	return p.Title + " " + p.Hint
}

func attemptsText(left int) string {
	return "Attempts left: " + strconv.Itoa(left)
}

func usernameText(account *adminv1.TelegramAccount) string {
	if account.Username == "" {
		return ""
	}
	return "@" + account.Username
}

func expiryText(remaining time.Duration) string {
	if remaining <= 0 {
		return codeExpiredText
	}
	seconds := int(remaining.Round(time.Second) / time.Second)
	return fmt.Sprintf("Code expires in %d:%02d", seconds/60, seconds%60)
}
