package shell

import "fyne.io/fyne/v2"

type appClipboard struct {
	app fyne.App
}

func (c appClipboard) SetText(text string) {
	c.app.Clipboard().SetContent(text)
}

func (c appClipboard) Text() string {
	return c.app.Clipboard().Content()
}
