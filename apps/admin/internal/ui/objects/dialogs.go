package objects

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

const dismissLabel = "Close"

type appClipboard struct{}

func (appClipboard) SetText(text string) {
	fyne.CurrentApp().Clipboard().SetContent(text)
}

func (appClipboard) Text() string {
	return fyne.CurrentApp().Clipboard().Content()
}

func newInfoDialog(title string, content fyne.CanvasObject, win fyne.Window) *dialog.CustomDialog {
	dlg := dialog.NewCustom(title, dismissLabel, content, win)
	return dlg
}

func newChoiceDialog(win fyne.Window, title string, content fyne.CanvasObject, buttons ...*widget.Button) *dialog.CustomDialog {
	dlg := dialog.NewCustomWithoutButtons(title, content, win)
	objects := make([]fyne.CanvasObject, 0, len(buttons))
	for _, button := range buttons {
		objects = append(objects, button)
	}
	dlg.SetButtons(objects)
	return dlg
}
