package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

const dialogLabel = "dialog"

type Dialog struct {
	popup    *widget.PopUp
	card     *WindowCard
	buttons  *fyne.Container
	onClosed []func()
}

func NewDialog(win fyne.Window, label, title string, content fyne.CanvasObject) *Dialog {
	return newDialog(win, label, title, content, NewWindowCard)
}

func NewBandDialog(win fyne.Window, label, title string, content fyne.CanvasObject) *Dialog {
	return newDialog(win, label, title, content, NewHeaderBandCard)
}

func newDialog(win fyne.Window, label, title string, content fyne.CanvasObject, card func(string, string, fyne.CanvasObject) *WindowCard) *Dialog {
	if label == "" {
		label = dialogLabel
	}
	d := &Dialog{buttons: container.NewHBox()}
	footer := container.NewVBox(VerticalSpace(2*gridUnit), container.NewBorder(nil, nil, nil, d.buttons))
	body := container.NewBorder(nil, footer, nil, nil, content)
	d.card = card(label, title, container.New(readableWidthLayout{}, body))
	d.popup = widget.NewModalPopUp(d.card, win.Canvas())
	return d
}

func (d *Dialog) SetButtons(buttons ...fyne.CanvasObject) {
	framed := make([]fyne.CanvasObject, 0, len(buttons))
	for _, b := range buttons {
		framed = append(framed, Action(b))
	}
	d.buttons.Objects = framed
	d.buttons.Refresh()
}

func (d *Dialog) SetOnClosed(fn func()) {
	d.onClosed = append(d.onClosed, fn)
}

func (d *Dialog) Show() {
	d.popup.Show()
}

func (d *Dialog) Hide() {
	d.popup.Hide()
	for _, fn := range d.onClosed {
		fn()
	}
}

func (d *Dialog) Card() *WindowCard {
	return d.card
}
