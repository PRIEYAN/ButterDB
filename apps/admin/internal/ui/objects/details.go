package objects

import (
	"sort"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	emptyDetailsText   = "Select an object to see its details."
	loadingDetailsText = "Loading details…"
)

type focusFrame struct {
	widget.BaseWidget
	content fyne.CanvasObject
}

func newFocusFrame(content fyne.CanvasObject) *focusFrame {
	f := &focusFrame{content: content}
	f.ExtendBaseWidget(f)
	return f
}

func (f *focusFrame) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(f.content)
}

func (f *focusFrame) FocusGained() {}

func (f *focusFrame) FocusLost() {}

func (f *focusFrame) TypedRune(rune) {}

func (f *focusFrame) TypedKey(*fyne.KeyEvent) {}

type detailsPanel struct {
	root   fyne.CanvasObject
	clip   model.Clipboard
	fields *fyne.Container
	status *widget.Label
	frame  *focusFrame
}

func newDetailsPanel(clip model.Clipboard) *detailsPanel {
	d := &detailsPanel{clip: clip}
	d.status = widget.NewLabel(emptyDetailsText)
	d.status.Wrapping = fyne.TextWrapWord
	d.fields = container.NewVBox()
	d.frame = newFocusFrame(container.NewVBox(d.status, d.fields))
	d.root = container.NewVScroll(d.frame)
	return d
}

func (d *detailsPanel) focusTarget() fyne.Focusable {
	return d.frame
}

func (d *detailsPanel) render(st vm.State) {
	d.fields.Objects = nil
	switch {
	case st.Details.Key == "":
		d.status.SetText(emptyDetailsText)
	case st.Details.Loading:
		d.status.SetText(loadingDetailsText)
	case st.Details.Problem != nil:
		d.status.SetText(st.Details.Problem.Title)
	case st.Details.Meta != nil:
		d.status.SetText("")
		for _, field := range metaFields(*st.Details.Meta) {
			d.fields.Add(shell.NewStackedCopyField(field.label, field.value, d.clip))
		}
	}
	d.fields.Refresh()
}

type metaField struct {
	label string
	value string
}

func metaFields(m adminv1.ObjectMeta) []metaField {
	encryption := "Not encrypted"
	if m.Encrypted {
		encryption = "Encrypted"
	}
	replicated := "No"
	if m.Replicated {
		replicated = "Yes"
	}
	fields := []metaField{
		{"Key", m.Key},
		{"Size", shell.FormatBytes(m.SizeBytes) + " (" + strconv.FormatInt(m.SizeBytes, 10) + " bytes)"},
		{"Stored size", shell.FormatBytes(m.StoredBytes) + " (" + strconv.FormatInt(m.StoredBytes, 10) + " bytes)"},
		{"Content type", m.ContentType},
		{"Last modified", m.LastModified.Local().Format(dateLayout)},
		{"ETag", m.ETag},
		{"SHA-256", m.SHA256},
		{"Encryption", encryption},
		{"Version ID", m.VersionID},
		{"Replicated", replicated},
	}
	names := make([]string, 0, len(m.UserMetadata))
	for name := range m.UserMetadata {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fields = append(fields, metaField{"Metadata " + name, m.UserMetadata[name]})
	}
	return fields
}
