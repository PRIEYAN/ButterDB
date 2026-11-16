package shell

import (
	"fmt"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const readableWidthEms = 28

type readableWidthLayout struct{}

func (readableWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		size = size.Max(o.MinSize())
	}
	size.Width = max(size.Width, theme.TextSize()*readableWidthEms)
	return size
}

func (readableWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func newWrappedLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func NewCopyField(label string, value string, clip model.Clipboard) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	text := widget.NewLabel(value)
	text.Selectable = true
	text.Wrapping = fyne.TextWrapBreak
	copyButton := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), nil)
	copyButton.OnTapped = func() {
		clip.SetText(value)
		copyButton.SetText("Copied")
	}
	return container.NewBorder(nil, nil, name, copyButton, text)
}

func NewProblemView(p model.Problem, clip model.Clipboard) fyne.CanvasObject {
	rows := []fyne.CanvasObject{
		widget.NewLabelWithStyle(p.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	}
	rows[0].(*widget.Label).Wrapping = fyne.TextWrapWord
	if p.Body != "" {
		rows = append(rows, newWrappedLabel(p.Body))
	}
	if p.Hint != "" {
		rows = append(rows, newWrappedLabel(p.Hint))
	}
	if text := detailsText(p); text != "" {
		rows = append(rows, newDetailsExpander(text, clip))
	}
	return container.New(readableWidthLayout{}, container.NewVBox(rows...))
}

func newDetailsExpander(text string, clip model.Clipboard) fyne.CanvasObject {
	body := widget.NewLabel(text)
	body.Selectable = true
	body.Wrapping = fyne.TextWrapBreak
	copyButton := widget.NewButtonWithIcon("Copy details", theme.ContentCopyIcon(), func() {
		clip.SetText(text)
	})
	return widget.NewAccordion(widget.NewAccordionItem("Details", container.NewVBox(body, copyButton)))
}

func detailsText(p model.Problem) string {
	var lines []string
	if p.Code != "" {
		lines = append(lines, "code: "+string(p.Code))
	}
	if status, ok := p.Details["http_status"]; ok {
		lines = append(lines, fmt.Sprintf("http status: %v", status))
	}
	if p.RequestID != "" {
		lines = append(lines, "request id: "+p.RequestID)
	}
	keys := make([]string, 0, len(p.Details))
	for key := range p.Details {
		if key != "http_status" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %v", key, p.Details[key]))
	}
	return strings.Join(lines, "\n")
}
