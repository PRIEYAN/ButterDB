package connect

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type actionButton struct {
	widget.Button
	onEscape func()
}

func newActionButton(label string, icon fyne.Resource, onTapped func()) *actionButton {
	b := &actionButton{}
	b.Text = label
	b.Icon = icon
	b.OnTapped = onTapped
	b.ExtendBaseWidget(b)
	return b
}

func (b *actionButton) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		if b.onEscape != nil {
			b.onEscape()
		}
	case fyne.KeyReturn, fyne.KeyEnter:
		b.Tapped(nil)
	default:
		b.Button.TypedKey(ev)
	}
}

func newWrappedLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func newHeading(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.Wrapping = fyne.TextWrapWord
	return label
}

func newMonospaceLabel(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	label.Selectable = true
	return label
}

func focusOn(object fyne.Focusable) {
	canvasObject, ok := object.(fyne.CanvasObject)
	if !ok {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(canvasObject); c != nil {
		c.Focus(object)
	}
}
