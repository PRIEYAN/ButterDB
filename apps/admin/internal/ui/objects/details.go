package objects

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	emptyDetailsText    = "Select an object to see its details."
	loadingDetailsText  = "Loading details…"
	previewTooManyPixel = "Too many pixels to preview (limit 40 megapixels)"
	previewDecodeFailed = "This image cannot be previewed"
	previewHeight       = 260
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
	root    fyne.CanvasObject
	clip    model.Clipboard
	fields  *fyne.Container
	status  *widget.Label
	image   *canvas.Image
	caption *widget.Label
	frame   *focusFrame
}

func newDetailsPanel(clip model.Clipboard) *detailsPanel {
	d := &detailsPanel{clip: clip}
	d.status = widget.NewLabel(emptyDetailsText)
	d.status.Wrapping = fyne.TextWrapWord
	d.fields = container.NewVBox()
	d.image = canvas.NewImageFromImage(nil)
	d.image.FillMode = canvas.ImageFillContain
	d.image.ScaleMode = canvas.ImageScaleSmooth
	d.image.SetMinSize(fyne.NewSize(0, previewHeight))
	d.image.Hide()
	d.caption = widget.NewLabel("")
	d.caption.Wrapping = fyne.TextWrapWord
	d.frame = newFocusFrame(container.NewVBox(shell.NewCodeBlock(d.image), d.caption))
	d.root = container.NewVScroll(container.NewVBox(d.status, d.frame, d.fields))
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

func (d *detailsPanel) showImage(img image.Image) {
	d.image.Image = img
	d.image.Show()
	d.image.Refresh()
	d.caption.SetText("")
}

func (d *detailsPanel) showCaption(text string) {
	d.image.Hide()
	d.caption.SetText(text)
}

func (b *browser) renderPreview(st vm.State) {
	switch st.Preview.Status {
	case vm.PreviewReady:
		b.startDecode(st.Preview)
	case vm.PreviewLoading:
		b.resetPreviewKey()
		b.details.showCaption("Loading preview…")
	case vm.PreviewUnsupported, vm.PreviewTooLarge:
		b.resetPreviewKey()
		b.details.showCaption(st.Preview.Message)
	case vm.PreviewFailed:
		b.resetPreviewKey()
		title := "Cannot load the preview."
		if st.Preview.Problem != nil {
			title = st.Preview.Problem.Title
		}
		b.details.showCaption(title)
	default:
		b.resetPreviewKey()
		b.details.showCaption("")
	}
}

func (b *browser) resetPreviewKey() {
	b.mu.Lock()
	b.previewKey = ""
	b.mu.Unlock()
}

func (b *browser) startDecode(p vm.Preview) {
	b.mu.Lock()
	already := b.previewKey == p.Key
	b.previewKey = p.Key
	b.mu.Unlock()
	if already {
		return
	}
	b.details.showCaption("Loading preview…")
	go b.decodePreview(p)
}

func (b *browser) decodePreview(p vm.Preview) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(p.Data))
	if err != nil {
		b.vmodel.MarkPreviewFailed(p.Key, previewDecodeFailed)
		return
	}
	if vm.ExceedsPixelCeiling(cfg.Width, cfg.Height) {
		b.vmodel.MarkPreviewFailed(p.Key, previewTooManyPixel)
		return
	}
	img, _, err := image.Decode(bytes.NewReader(p.Data))
	if err != nil {
		b.vmodel.MarkPreviewFailed(p.Key, previewDecodeFailed)
		return
	}
	b.ui(func() {
		b.mu.Lock()
		current := b.previewKey == p.Key
		b.mu.Unlock()
		if current {
			b.details.showImage(img)
		}
	})
}
