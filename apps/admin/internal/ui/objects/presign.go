package objects

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	selectObjectHint = "Select an object first."
	signingKeyField  = "signing_key"
	loadingKeysText  = "Loading access keys…"
)

type presignDialog struct {
	owner    *browser
	bucket   string
	key      string
	keys     []vm.SigningKey
	signing  *widget.Select
	expiry   *widget.Select
	custom   *widget.Entry
	problem  *widget.Label
	result   *widget.Label
	expires  *widget.Label
	copyURL  *widget.Button
	generate *widget.Button
	options  []vm.ExpiryOption
}

func (b *browser) presignKey() string {
	if row, ok := b.focusedRow(); ok && row.Kind == vm.RowObject {
		return row.Key
	}
	if selected := b.snapshot().SelectedObjects; len(selected) == 1 {
		return selected[0]
	}
	return ""
}

func (b *browser) openPresignDialog() {
	key := b.presignKey()
	bucket, _ := b.vmodel.Location()
	if key == "" || bucket == "" {
		b.inform(selectObjectHint)
		return
	}
	d := &presignDialog{owner: b, bucket: bucket, key: key, options: vm.ExpiryOptions()}
	d.show()
}

func (d *presignDialog) show() {
	d.signing = widget.NewSelect(nil, nil)
	d.signing.PlaceHolder = loadingKeysText
	d.custom = widget.NewEntry()
	d.custom.SetPlaceHolder("Seconds (1 to " + strconv.Itoa(vm.MaxPresignSeconds) + ")")
	d.custom.Hide()
	d.expiry = widget.NewSelect(d.expiryLabels(), d.onExpiryChanged)
	d.expiry.SetSelected(d.options[1].Label)
	d.problem = widget.NewLabel("")
	d.problem.Wrapping = fyne.TextWrapWord
	d.result = widget.NewLabel("")
	d.result.Wrapping = fyne.TextWrapBreak
	d.result.Selectable = true
	d.expires = widget.NewLabel("")
	d.copyURL = widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() { d.owner.clip.SetText(d.result.Text) })
	d.copyURL.Hide()
	d.generate = widget.NewButtonWithIcon("Generate", theme.ConfirmIcon(), d.onGenerate)
	d.generate.Disable()
	object := widget.NewLabel(d.key)
	object.Selectable = true
	object.Wrapping = fyne.TextWrapBreak
	warning := widget.NewLabel(vm.PresignWarning)
	warning.Wrapping = fyne.TextWrapWord
	form := widget.NewForm(
		widget.NewFormItem("Object", object),
		widget.NewFormItem("Signing key", d.signing),
		widget.NewFormItem("Expires in", container.NewVBox(d.expiry, d.custom)),
	)
	d.result.TextStyle = fyne.TextStyle{Monospace: true}
	d.expires.TextStyle = fyne.TextStyle{Monospace: true}
	content := container.NewVBox(
		form, d.problem,
		container.NewHBox(shell.Action(d.generate)),
		shell.NewCodeBlock(d.result), d.expires,
		container.NewHBox(shell.Action(d.copyURL)),
		shell.NewCallout(shell.CalloutWarning, warning),
	)
	dlg := newInfoDialog("objects/presign", "Copy presigned URL", content, d.owner.win)
	dlg.Show()
	go d.loadKeys()
}

func (d *presignDialog) expiryLabels() []string {
	labels := make([]string, 0, len(d.options)+1)
	for _, option := range d.options {
		labels = append(labels, option.Label)
	}
	return append(labels, vm.CustomExpiryLabel)
}

func (d *presignDialog) onExpiryChanged(label string) {
	if label == vm.CustomExpiryLabel {
		d.custom.Show()
		return
	}
	d.custom.Hide()
}

func (d *presignDialog) loadKeys() {
	keys, problem := d.owner.vmodel.SigningKeys(d.owner.commandContext(), d.bucket)
	d.owner.ui(func() {
		if problem != nil {
			d.problem.SetText(problem.Title)
			return
		}
		d.keys = keys
		if len(keys) == 0 {
			d.signing.PlaceHolder = vm.NoSigningKeysText
			d.signing.Refresh()
			d.problem.SetText(vm.NoSigningKeysText)
			return
		}
		names := make([]string, 0, len(keys))
		for _, key := range keys {
			names = append(names, signingKeyLabel(key))
		}
		d.signing.SetOptions(names)
		d.signing.SetSelected(names[0])
		d.generate.Enable()
	})
}

func signingKeyLabel(key vm.SigningKey) string {
	if key.Name == "" {
		return key.ID
	}
	return key.Name + " (" + key.ID + ")"
}

func (d *presignDialog) selectedKeyID() string {
	for _, key := range d.keys {
		if signingKeyLabel(key) == d.signing.Selected {
			return key.ID
		}
	}
	return ""
}

func (d *presignDialog) expirySeconds() int {
	if d.expiry.Selected == vm.CustomExpiryLabel {
		seconds, err := strconv.Atoi(strings.TrimSpace(d.custom.Text))
		if err != nil {
			return 0
		}
		return seconds
	}
	for _, option := range d.options {
		if option.Label == d.expiry.Selected {
			return option.Seconds
		}
	}
	return vm.DefaultPresignSeconds
}

func (d *presignDialog) onGenerate() {
	keyID, seconds := d.selectedKeyID(), d.expirySeconds()
	d.problem.SetText("")
	go func() {
		out := d.owner.vmodel.Presign(d.owner.commandContext(), d.bucket, d.key, keyID, seconds)
		d.owner.ui(func() { d.render(out) })
	}()
}

func (d *presignDialog) render(out vm.PresignOutcome) {
	if out.Problem != nil {
		d.renderProblem(*out.Problem)
		return
	}
	d.result.SetText(out.URL.URL)
	d.expires.SetText("Expires at " + out.URL.ExpiresAt.Local().Format(dateLayout))
	d.copyURL.Show()
}

func (d *presignDialog) renderProblem(p model.Problem) {
	if p.Presentation == model.PresentDialog || p.Presentation == model.PresentRoute {
		d.owner.present(p)
		return
	}
	d.problem.SetText(p.Title)
}
