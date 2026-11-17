package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const fileDialogPadding = 2 * gridUnit

func ShowFileDialog(win fyne.Window, dlg *dialog.FileDialog) {
	dlg.Show()
	popup, ok := win.Canvas().Overlays().Top().(*widget.PopUp)
	if !ok || popup.Content == nil {
		return
	}
	if _, framed := popup.Content.(*fileDialogFrame); framed {
		return
	}
	// A popup keeps drawing the content it was built with, so the dialog UI moves into a new framed popup.
	ui := popup.Content
	size := ui.Size()
	popup.Hide()
	framed := widget.NewModalPopUp(newFileDialogFrame(ui), win.Canvas())
	dlg.SetOnClosed(framed.Hide)
	framed.Resize(size.AddWidthHeight(2*fileDialogPadding+theme.Size(theme.SizeNameInnerPadding), 2*fileDialogPadding+theme.Size(theme.SizeNameInnerPadding)))
	framed.Show()
}

type fileDialogFrame struct {
	widget.BaseWidget
	content fyne.CanvasObject
	border  *canvas.Rectangle
}

func newFileDialogFrame(content fyne.CanvasObject) *fileDialogFrame {
	border := canvas.NewRectangle(color.Transparent)
	border.StrokeColor = color.Black
	border.StrokeWidth = 1
	f := &fileDialogFrame{content: content, border: border}
	f.ExtendBaseWidget(f)
	return f
}

func (f *fileDialogFrame) CreateRenderer() fyne.WidgetRenderer {
	return &fileDialogFrameRenderer{f: f}
}

type fileDialogFrameRenderer struct{ f *fileDialogFrame }

func (r *fileDialogFrameRenderer) Layout(size fyne.Size) {
	r.f.content.Move(fyne.NewPos(fileDialogPadding, fileDialogPadding))
	r.f.content.Resize(size.SubtractWidthHeight(2*fileDialogPadding, 2*fileDialogPadding))
	// The popup background extends half its inner padding past this frame; the border traces that edge.
	inset := theme.Size(theme.SizeNameInnerPadding) / 2
	r.f.border.Move(fyne.NewPos(-inset, -inset))
	r.f.border.Resize(size.AddWidthHeight(2*inset, 2*inset))
}

func (r *fileDialogFrameRenderer) MinSize() fyne.Size {
	return r.f.content.MinSize().AddWidthHeight(2*fileDialogPadding, 2*fileDialogPadding)
}
func (r *fileDialogFrameRenderer) Refresh() {
	r.f.border.Refresh()
	r.f.content.Refresh()
}
func (r *fileDialogFrameRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.f.content, r.f.border}
}
func (r *fileDialogFrameRenderer) Destroy() {}
