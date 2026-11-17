package shell

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestShowFileDialogDrawsFramedPaddedPopupAndCloses(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("x"))
	win.Resize(fyne.NewSize(1400, 1000))
	dlg := dialog.NewFileOpen(func(fyne.URIReadCloser, error) {}, win)
	ShowFileDialog(win, dlg)
	overlays := win.Canvas().Overlays().List()
	if len(overlays) != 1 {
		t.Fatalf("overlays = %d, want only the framed popup", len(overlays))
	}
	popup := overlays[0].(*widget.PopUp)
	frame, ok := popup.Content.(*fileDialogFrame)
	if !ok {
		t.Fatalf("popup content = %T, want framed dialog", popup.Content)
	}
	if frame.border.StrokeWidth != 1 || frame.border.StrokeColor != color.Black {
		t.Fatalf("border = %v width %v, want 1px black", frame.border.StrokeColor, frame.border.StrokeWidth)
	}
	if frame.Position().X <= 0 || frame.Position().Y <= 0 {
		t.Fatalf("frame at %v, want centred in the window", frame.Position())
	}
	if got := frame.content.Position(); got != fyne.NewPos(fileDialogPadding, fileDialogPadding) {
		t.Fatalf("content at %v, want inset by padding", got)
	}
	if abs := fyne.CurrentApp().Driver().AbsolutePositionForObject(frame.content); abs.X <= fileDialogPadding {
		t.Fatalf("dialog UI drawn at %v, want inside the centred frame", abs)
	}
	dlg.Dismiss()
	if top := win.Canvas().Overlays().Top(); top != nil {
		t.Fatalf("overlay %T still shown after cancel", top)
	}
}
