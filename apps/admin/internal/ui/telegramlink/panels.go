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
	return container.NewVBox(shell.NewHighlightCard("telegram/account-safety", safetyNoticeTitle, wrapped(safetyNotice)), shell.NewSectionGap())
}

func stepCard(label string, rows ...fyne.CanvasObject) fyne.CanvasObject {
	return shell.NewWindowCard(label, "", container.NewVBox(rows...))
}

func primary(button *widget.Button) fyne.CanvasObject {
	return container.NewHBox(shell.Action(button))
}

func (w *LoginWizard) newInline(fields ...vm.Field) fyne.CanvasObject {
	label := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.Importance = widget.DangerImportance
	label.Wrapping = fyne.TextWrapWord
	label.Hide()
	for _, field := range fields {
		w.inline[field] = label
	}
	return shell.NewCallout(shell.CalloutDanger, label)
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
		stepCard("telegram/app-credentials",
			wrapped(appHelp),
			portal,
			form,
			w.newInline(vm.FieldAPIID, vm.FieldAPIHash),
			shell.NewSectionGap(),
			primary(p.submit),
		),
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
		stepCard("telegram/phone",
			wrapped(phoneHelp),
			widget.NewForm(widget.NewFormItem(labelPhone, p.entry)),
			w.newInline(vm.FieldPhone),
			shell.NewSectionGap(),
			container.NewHBox(shell.Action(p.submit), p.change),
		),
	)
}

func (w *LoginWizard) buildWait() fyne.CanvasObject {
	w.wait.text = wrapped(waitText)
	return stepCard("telegram/sending-code", w.wait.text)
}

func (w *LoginWizard) buildCode() fyne.CanvasObject {
	p := &w.code
	p.delivery = wrapped("")
	p.phone = shell.NewMono("")
	p.attempts = shell.NewMono("")
	p.expires = shell.NewMono("")
	p.entry = widget.NewEntry()
	p.entry.OnChanged = func(text string) {
		if p.length > 0 && len([]rune(text)) > p.length {
			p.entry.SetText(string([]rune(text)[:p.length]))
		}
	}
	p.entry.OnSubmitted = func(string) { w.submitCode() }
	p.submit = widget.NewButtonWithIcon(buttonSubmitCode, theme.ConfirmIcon(), w.submitCode)
	p.submit.Importance = widget.HighImportance
	p.entry.TextStyle = fyne.TextStyle{Monospace: true}
	return stepCard("telegram/login-code",
		p.delivery,
		shell.NewCodeBlock(container.NewVBox(p.phone, p.expires)),
		widget.NewForm(widget.NewFormItem(labelCode, p.entry)),
		p.attempts,
		w.newInline(vm.FieldCode),
		shell.NewSectionGap(),
		primary(p.submit),
	)
}

func (w *LoginWizard) buildPassword() fyne.CanvasObject {
	p := &w.password
	p.hint = wrapped("")
	p.attempts = shell.NewMono("")
	p.entry = widget.NewEntry()
	p.entry.Password = true
	p.entry.OnSubmitted = func(string) { w.submitPassword() }
	show := widget.NewCheck(labelShow, func(on bool) {
		p.entry.Password = !on
		p.entry.Refresh()
	})
	p.submit = widget.NewButtonWithIcon(buttonSubmitPass, theme.ConfirmIcon(), w.submitPassword)
	p.submit.Importance = widget.HighImportance
	return stepCard("telegram/two-step-password",
		wrapped(passwordHelp),
		shell.NewCallout(shell.CalloutInfo, p.hint),
		widget.NewForm(widget.NewFormItem(labelPassword, p.entry)),
		show,
		p.attempts,
		w.newInline(vm.FieldPassword),
		shell.NewSectionGap(),
		primary(p.submit),
	)
}

func (w *LoginWizard) buildDone() fyne.CanvasObject {
	p := &w.done
	p.name = shell.NewSectionTitle("")
	p.username = shell.NewMono("")
	p.phone = shell.NewMono("")
	p.next = widget.NewButtonWithIcon(buttonContinue, theme.NavigateNextIcon(), w.finish)
	p.next.Importance = widget.HighImportance
	return stepCard("telegram/linked", p.name, p.username, p.phone, shell.NewSectionGap(), primary(p.next))
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
