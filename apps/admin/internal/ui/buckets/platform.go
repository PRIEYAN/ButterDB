package buckets

import "fyne.io/fyne/v2"

type appClipboard struct{}

func (appClipboard) SetText(text string) { fyne.CurrentApp().Clipboard().SetContent(text) }

func (appClipboard) Text() string { return fyne.CurrentApp().Clipboard().Content() }

func windowOf(obj fyne.CanvasObject) fyne.Window {
	driver := fyne.CurrentApp().Driver()
	windows := driver.AllWindows()
	if len(windows) == 0 {
		return nil
	}
	canvas := driver.CanvasForObject(obj)
	for _, w := range windows {
		if w.Canvas() == canvas {
			return w
		}
	}
	return nil
}
