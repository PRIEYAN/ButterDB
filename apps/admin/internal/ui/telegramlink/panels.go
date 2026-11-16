package telegramlink

import (
	"context"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

type appPanel struct {
	id     *widget.Entry
	hash   *widget.Entry
	submit *widget.Button
	notice fyne.CanvasObject
}

type phonePanel struct {
	entry  *widget.Entry
	submit *widget.Button
	change *widget.Button
}

type waitPanel struct {
	text *widget.Label
}

type codePanel struct {
	entry    *widget.Entry
	submit   *widget.Button
	delivery *widget.Label
	phone    *widget.Label
	attempts *widget.Label
	expires  *widget.Label
	length   int
}

type passwordPanel struct {
	entry    *widget.Entry
	submit   *widget.Button
	hint     *widget.Label
	attempts *widget.Label
}

type donePanel struct {
	name     *widget.Label
	username *widget.Label
	phone    *widget.Label
	next     *widget.Button
}

func wrapped(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func newNotice() fyne.CanvasObject {
	return widget.NewCard(safetyNoticeTitle, "", wrapped(safetyNotice))
}

func (w *LoginWizard) newInline(fields ...vm.Field) *widget.Label {
	label := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.Importance = widget.DangerImportance
	label.Wrapping = fyne.TextWrapWord
	label.Hide()
	for _, field := range fields {
		w.inline[field] = label
	}
	return label
}

func (w *LoginWizard) buildApp() fyne.CanvasObject {
	p := &w.app
	p.id = widget.NewEntry()
	p.id.SetPlaceHolder(placeholderAPIID)
	p.id.OnChanged = func(text string) {
		if digits := digitsOnly(text); digits != text {
			p.id.SetText(digits)
		}
	}
	p.hash = widget.NewPasswordEntry()
	p.hash.SetPlaceHolder(placeholderAPIHash)
	p.hash.OnSubmitted = func(string) { w.submitApp() }
	p.submit = widget.NewButtonWithIcon(buttonSaveApp, theme.ConfirmIcon(), w.submitApp)
	p.submit.Importance = widget.HighImportance
	p.notice = newNotice()
	portal := widget.NewHyperlink(appPortalLabel, mustURL(appPortalURL))
	form := widget.NewForm(widget.NewFormItem(labelAPIID, p.id), widget.NewFormItem(labelAPIHash, p.hash))
	return container.NewVBox(
		p.notice,
		wrapped(appHelp),
		portal,
		form,
		w.newInline(vm.FieldAPIID, vm.FieldAPIHash),
		p.submit,
	)
}

func (w *LoginWizard) buildPhone() fyne.CanvasObject {
	p := &w.phone
	p.entry = widget.NewEntry()
	p.entry.SetPlaceHolder(placeholderPhone)
	p.entry.OnChanged = func(text string) {
		if text != "" && !strings.HasPrefix(text, "+") {
			p.entry.SetText("+" + text)
		}
	}
	p.entry.OnSubmitted = func(string) { w.submitPhone() }
	p.submit = widget.NewButtonWithIcon(buttonSendCode, theme.MailSendIcon(), w.submitPhone)
	p.submit.Importance = widget.HighImportance
	p.change = widget.NewButton(buttonChangeApp, w.confirmChangeApp)
	p.change.Importance = widget.LowImportance
	notice := container.NewVBox()
	if !w.opts.AskAppCredentials {
		notice.Add(newNotice())
	}
	return container.NewVBox(
		notice,
		wrapped(phoneHelp),
		widget.NewForm(widget.NewFormItem(labelPhone, p.entry)),
		w.newInline(vm.FieldPhone),
		p.submit,
		p.change,
	)
}

func (w *LoginWizard) buildWait() fyne.CanvasObject {
	w.wait.text = wrapped(waitText)
	return container.NewVBox(w.wait.text)
}

func (w *LoginWizard) buildCode() fyne.CanvasObject {
	p := &w.code
	p.delivery = wrapped("")
	p.phone = widget.NewLabel("")
	p.attempts = widget.NewLabel("")
	p.expires = widget.NewLabel("")
	p.entry = widget.NewEntry()
	p.entry.OnChanged = func(text string) {
		if p.length > 0 && len([]rune(text)) > p.length {
			p.entry.SetText(string([]rune(text)[:p.length]))
		}
	}
	p.entry.OnSubmitted = func(string) { w.submitCode() }
	p.submit = widget.NewButtonWithIcon(buttonSubmitCode, theme.ConfirmIcon(), w.submitCode)
	p.submit.Importance = widget.HighImportance
	return container.NewVBox(
		p.delivery,
		p.phone,
		p.expires,
		widget.NewForm(widget.NewFormItem(labelCode, p.entry)),
		p.attempts,
		w.newInline(vm.FieldCode),
		p.submit,
	)
}

func (w *LoginWizard) buildPassword() fyne.CanvasObject {
	p := &w.password
	p.hint = wrapped("")
	p.attempts = widget.NewLabel("")
	p.entry = widget.NewEntry()
	p.entry.Password = true
	p.entry.OnSubmitted = func(string) { w.submitPassword() }
	show := widget.NewCheck(labelShow, func(on bool) {
		p.entry.Password = !on
		p.entry.Refresh()
	})
	p.submit = widget.NewButtonWithIcon(buttonSubmitPass, theme.ConfirmIcon(), w.submitPassword)
	p.submit.Importance = widget.HighImportance
	return container.NewVBox(
		wrapped(passwordHelp),
		p.hint,
		widget.NewForm(widget.NewFormItem(labelPassword, p.entry)),
		show,
		p.attempts,
		w.newInline(vm.FieldPassword),
		p.submit,
	)
}

func (w *LoginWizard) buildDone() fyne.CanvasObject {
	p := &w.done
	p.name = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.username = widget.NewLabel("")
	p.phone = widget.NewLabel("")
	p.next = widget.NewButtonWithIcon(buttonContinue, theme.NavigateNextIcon(), w.finish)
	p.next.Importance = widget.HighImportance
	return container.NewVBox(p.name, p.username, p.phone, p.next)
}

func (w *LoginWizard) submitApp() {
	id, hash := w.app.id.Text, w.app.hash.Text
	w.app.hash.SetText("")
	w.dispatch(func(ctx context.Context) { w.v.SaveApp(ctx, id, hash) })
}

func (w *LoginWizard) submitPhone() {
	phone := w.phone.entry.Text
	w.dispatch(func(ctx context.Context) { w.v.StartLogin(ctx, phone) })
}

func (w *LoginWizard) submitCode() {
	code := w.code.entry.Text
	w.code.entry.SetText("")
	w.dispatch(func(ctx context.Context) { w.v.SubmitCode(ctx, code) })
}

func (w *LoginWizard) submitPassword() {
	password := w.password.entry.Text
	w.password.entry.SetText("")
	w.dispatch(func(ctx context.Context) { w.v.SubmitPassword(ctx, password) })
}

func (w *LoginWizard) confirmChangeApp() {
	if w.win == nil {
		return
	}
	shell.ConfirmDestructive(w.win, changeAppTitle, changeAppBody, changeAppConfirm, func() {
		w.dispatch(w.v.ChangeApp)
	})
}

func digitsOnly(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}
