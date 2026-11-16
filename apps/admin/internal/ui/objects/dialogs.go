package objects

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

const dismissLabel = "Close"

type appClipboard struct{}

func (appClipboard) SetText(text string) {
	fyne.CurrentApp().Clipboard().SetContent(text)
}

func (appClipboard) Text() string {
	return fyne.CurrentApp().Clipboard().Content()
}

func newInfoDialog(label, title string, content fyne.CanvasObject, win fyne.Window) *shell.Dialog {
	dlg := shell.NewDialog(win, label, title, content)
	dlg.SetButtons(widget.NewButton(dismissLabel, func() { dlg.Hide() }))
	return dlg
}

func newChoiceDialog(win fyne.Window, label, title string, content fyne.CanvasObject, buttons ...*widget.Button) *shell.Dialog {
	dlg := shell.NewDialog(win, label, title, content)
	objects := make([]fyne.CanvasObject, 0, len(buttons))
	for _, button := range buttons {
		objects = append(objects, button)
	}
	dlg.SetButtons(objects...)
	return dlg
}
