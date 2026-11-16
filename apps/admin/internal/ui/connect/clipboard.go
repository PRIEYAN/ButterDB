package connect

import "fyne.io/fyne/v2"

type appClipboard struct{}

func (appClipboard) SetText(text string) {
	fyne.CurrentApp().Clipboard().SetContent(text)
}

func (appClipboard) Text() string {
	return fyne.CurrentApp().Clipboard().Content()
}
