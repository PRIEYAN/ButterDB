package keys

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

const (
	secretTitle    = "Save this secret now"
	secretBody     = "The secret access key is shown only once. butterDB cannot show it again; if you lose it, revoke the key and create a new one."
	acknowledgeBox = "I have stored the secret"
	copyEnvLabel   = "Copy as environment variables"
	doneLabel      = "Done"
	secretEms      = 36
)

type secretDialog struct {
	popup *widget.PopUp
	ack   *widget.Check
	done  *widget.Button
	env   *widget.Button
}

func newSecretDialog(secret vm.Secret, clip model.Clipboard, canvasOf fyne.Canvas, onDone func()) *secretDialog {
	d := &secretDialog{}
	title := widget.NewLabelWithStyle(secretTitle, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := widget.NewLabel(secretBody)
	body.Wrapping = fyne.TextWrapWord
	fields := container.NewVBox(
		shell.NewCopyField("Access key ID", secret.AccessKeyID, clip),
		shell.NewCopyField("Secret access key", secret.SecretAccessKey, clip),
	)
	if secret.S3Endpoint != "" {
		fields.Add(shell.NewCopyField("S3 endpoint", secret.S3Endpoint, clip))
	}
	if secret.Region != "" {
		fields.Add(shell.NewCopyField("Region", secret.Region, clip))
	}
	d.env = widget.NewButtonWithIcon(copyEnvLabel, theme.ContentCopyIcon(), func() { clip.SetText(secret.EnvBlock()) })
	d.done = widget.NewButtonWithIcon(doneLabel, theme.ConfirmIcon(), func() {
		d.popup.Hide()
		onDone()
	})
	d.done.Disable()
	d.ack = widget.NewCheck(acknowledgeBox, func(checked bool) {
		if checked {
			d.done.Enable()
			return
		}
		d.done.Disable()
	})
	width := canvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	width.SetMinSize(fyne.NewSize(theme.TextSize()*secretEms, 0))
	content := container.NewStack(width, container.NewPadded(container.NewVBox(title, body, fields, d.env, d.ack, d.done)))
	d.popup = widget.NewModalPopUp(content, canvasOf)
	return d
}

func (d *secretDialog) show(c fyne.Canvas) {
	d.popup.Show()
	c.Focus(d.ack)
}
