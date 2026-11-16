package buckets

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
)

const (
	accessPrivate    = "Private"
	accessPublicRead = "Public read"
	encryptionOff    = "Off"
	encryptionOn     = "On (TDE1)"

	publicReadNotice  = "Anyone with the URL can read every object in this bucket."
	encryptionNotice  = "Encryption keys depend on the server key file. Back it up off the host; losing it makes these objects unrecoverable."
	encryptionNewOnly = "Encryption changes apply to new objects only."

	notANumber = -1
)

type bucketForm struct {
	creating   bool
	clip       model.Clipboard
	name       *widget.Entry
	access     *widget.RadioGroup
	encryption *widget.RadioGroup
	cache      *widget.Entry
	preset     *widget.Select
	accessNote *widget.Label
	encNote    *widget.Label
	nameError  *widget.Label
	cacheError *widget.Label
	formError  *fyne.Container
	progress   *widget.ProgressBarInfinite
	root       fyne.CanvasObject
}

func newBucketForm(initial vm.NewBucket, creating bool, clip model.Clipboard) *bucketForm {
	f := &bucketForm{creating: creating, clip: clip}
	f.name = widget.NewEntry()
	f.name.SetPlaceHolder("my-bucket")
	f.access = widget.NewRadioGroup([]string{accessPrivate, accessPublicRead}, func(string) { f.updateNotes() })
	f.access.Horizontal = true
	f.access.Required = true
	f.encryption = widget.NewRadioGroup([]string{encryptionOff, encryptionOn}, func(string) { f.updateNotes() })
	f.encryption.Horizontal = true
	f.encryption.Required = true
	f.cache = widget.NewEntry()
	f.preset = widget.NewSelect(presetLabels(), f.applyPreset)
	f.preset.PlaceHolder = "Presets"
	f.accessNote = newNote()
	f.encNote = newNote()
	f.nameError = newErrorLabel()
	f.cacheError = newErrorLabel()
	f.formError = container.NewStack()
	f.progress = widget.NewProgressBarInfinite()
	f.progress.Hide()
	f.name.OnChanged = func(string) { f.clearErrors() }
	f.cache.OnChanged = func(string) { f.cacheError.SetText("") }
	f.fill(initial)
	f.root = f.layout()
	return f
}

func newNote() *widget.Label {
	l := widget.NewLabel("")
	l.Wrapping = fyne.TextWrapWord
	l.Hide()
	return l
}

func setNote(l *widget.Label, text string) {
	l.SetText(text)
	if text == "" {
		l.Hide()
		return
	}
	l.Show()
}

func newErrorLabel() *widget.Label {
	l := widget.NewLabel("")
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.DangerImportance
	return l
}

func presetLabels() []string {
	presets := vm.CachePresets()
	labels := make([]string, len(presets))
	for i, p := range presets {
		labels[i] = p.Label
	}
	return labels
}

func (f *bucketForm) fill(initial vm.NewBucket) {
	f.name.SetText(initial.Name)
	f.access.SetSelected(accessPrivate)
	if initial.PublicRead {
		f.access.SetSelected(accessPublicRead)
	}
	f.encryption.SetSelected(encryptionOff)
	if initial.Encrypted {
		f.encryption.SetSelected(encryptionOn)
	}
	f.cache.SetText(strconv.Itoa(initial.CacheMaxAgeSeconds))
	f.updateNotes()
}

func (f *bucketForm) layout() fyne.CanvasObject {
	rows := []fyne.CanvasObject{}
	if f.creating {
		rows = append(rows, shell.NewFieldLabel("Name"), f.name, f.nameError)
	}
	rows = append(rows,
		shell.NewFieldLabel("Access"), f.access, shell.NewCallout(shell.CalloutWarning, f.accessNote),
		shell.NewFieldLabel("Encryption default"), f.encryption, shell.NewCallout(shell.CalloutWarning, f.encNote),
		shell.NewFieldLabel("Cache max-age in seconds"), container.NewBorder(nil, nil, nil, shell.FramedInput(f.preset), f.cache), f.cacheError,
	)
	if !f.creating {
		rows = append(rows, shell.NewCalloutText(shell.CalloutInfo, encryptionNewOnly))
	}
	rows = append(rows, f.formError, f.progress)
	return container.NewVBox(rows...)
}

func (f *bucketForm) updateNotes() {
	setNote(f.accessNote, "")
	if f.access.Selected == accessPublicRead {
		setNote(f.accessNote, publicReadNotice)
	}
	setNote(f.encNote, "")
	if f.encryption.Selected == encryptionOn {
		setNote(f.encNote, encryptionNotice)
	}
}

func (f *bucketForm) applyPreset(label string) {
	for _, p := range vm.CachePresets() {
		if p.Label == label {
			f.cache.SetText(strconv.Itoa(p.Seconds))
		}
	}
}

func (f *bucketForm) newBucket() vm.NewBucket {
	return vm.NewBucket{
		Name:               strings.TrimSpace(f.name.Text),
		PublicRead:         f.access.Selected == accessPublicRead,
		Encrypted:          f.encryption.Selected == encryptionOn,
		CacheMaxAgeSeconds: f.cacheSeconds(),
	}
}

func (f *bucketForm) settings() vm.Settings {
	b := f.newBucket()
	return vm.Settings{PublicRead: b.PublicRead, Encrypted: b.Encrypted, CacheMaxAgeSeconds: b.CacheMaxAgeSeconds}
}

func (f *bucketForm) cacheSeconds() int {
	text := strings.TrimSpace(f.cache.Text)
	if text == "" {
		return vm.DefaultCacheMaxAgeSeconds
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return notANumber
	}
	return n
}

func (f *bucketForm) clearErrors() {
	f.nameError.SetText("")
	f.cacheError.SetText("")
	f.formError.Objects = nil
	f.formError.Refresh()
}

func (f *bucketForm) setBusy(busy bool) {
	if busy {
		f.clearErrors()
		f.progress.Show()
		f.progress.Start()
		return
	}
	f.progress.Stop()
	f.progress.Hide()
}

func (f *bucketForm) showProblem(p *model.Problem) {
	f.clearErrors()
	switch {
	case p.Presentation == model.PresentInline && p.Field == vm.FieldName:
		f.nameError.SetText(inlineText(p))
	case p.Presentation == model.PresentInline && p.Field == vm.FieldCacheMaxAge:
		f.cacheError.SetText(inlineText(p))
	default:
		f.formError.Objects = []fyne.CanvasObject{shell.NewProblemView(*p, f.clip)}
		f.formError.Refresh()
	}
}

func (f *bucketForm) focusFirst(c fyne.Canvas) {
	if f.creating {
		c.Focus(f.name)
		return
	}
	c.Focus(f.cache)
}
