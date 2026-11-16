package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type actionFrame struct {
	widget.BaseWidget
	control  fyne.CanvasObject
	override *container.ThemeOverride
	border   *outline
	hover    *hoverSurface
	inverted *invertedTheme
	borderOf fyne.ThemeColorName
}

func Action(control fyne.CanvasObject) fyne.CanvasObject {
	return newActionFrame(control, colorNameBorderStrong, true)
}

func FramedInput(control fyne.CanvasObject) fyne.CanvasObject {
	return newActionFrame(control, theme.ColorNameInputBorder, false)
}

func Actions(controls ...fyne.CanvasObject) *fyne.Container {
	framed := make([]fyne.CanvasObject, 0, len(controls))
	for _, c := range controls {
		framed = append(framed, Action(c))
	}
	return container.NewHBox(framed...)
}

func newActionFrame(control fyne.CanvasObject, borderOf fyne.ThemeColorName, invertOnHover bool) *actionFrame {
	f := &actionFrame{control: control, borderOf: borderOf, border: newOutline()}
	if invertOnHover {
		f.inverted = &invertedTheme{}
		f.override = container.NewThemeOverride(control, f.inverted)
		f.hover = newHoverSurface(f.setHovered)
	}
	f.ExtendBaseWidget(f)
	return f
}

func (f *actionFrame) setHovered(on bool) {
	if f.inverted.hovered == on {
		return
	}
	f.inverted.hovered = on
	f.override.Refresh()
}

func (f *actionFrame) Visible() bool {
	return f.BaseWidget.Visible() && f.control.Visible()
}

func (f *actionFrame) CreateRenderer() fyne.WidgetRenderer {
	layers := []fyne.CanvasObject{f.control}
	if f.override != nil {
		layers = []fyne.CanvasObject{f.override, f.hover}
	}
	objects := append([]fyne.CanvasObject{layers[0]}, f.border.objects()...)
	objects = append(objects, layers[1:]...)
	r := &actionFrameRenderer{frame: f, layers: layers, objects: objects}
	r.applyTheme()
	return r
}

type actionFrameRenderer struct {
	frame   *actionFrame
	layers  []fyne.CanvasObject
	objects []fyne.CanvasObject
}

func (r *actionFrameRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *actionFrameRenderer) Destroy() {}

func (r *actionFrameRenderer) Layout(size fyne.Size) {
	for _, o := range r.layers {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(clampSize(size))
	}
	r.frame.border.place(size)
}

func (r *actionFrameRenderer) MinSize() fyne.Size {
	return r.frame.control.MinSize()
}

func (r *actionFrameRenderer) Refresh() {
	r.applyTheme()
	for _, o := range r.objects {
		o.Refresh()
	}
}

func (r *actionFrameRenderer) applyTheme() {
	f := r.frame
	f.border.setColor(designColor(f.Theme(), f.borderOf, fyne.CurrentApp().Settings().ThemeVariant()))
}

type hoverSurface struct {
	widget.BaseWidget
	onHover func(bool)
}

func newHoverSurface(onHover func(bool)) *hoverSurface {
	h := &hoverSurface{onHover: onHover}
	h.ExtendBaseWidget(h)
	return h
}

func (h *hoverSurface) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

func (h *hoverSurface) MouseIn(*desktop.MouseEvent) { h.onHover(true) }

func (h *hoverSurface) MouseMoved(*desktop.MouseEvent) {}

func (h *hoverSurface) MouseOut() { h.onHover(false) }

type invertedTheme struct {
	hovered bool
}

func (t *invertedTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	base := theme.Current()
	if !t.hovered {
		return base.Color(name, v)
	}
	switch name {
	case theme.ColorNameButton, theme.ColorNamePrimary, theme.ColorNameError, theme.ColorNameSuccess, theme.ColorNameWarning:
		return designColor(base, colorNameAccent, v)
	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnError,
		theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning:
		return designColor(base, colorNameOnAccent, v)
	case theme.ColorNameHover:
		return color.Transparent
	}
	return base.Color(name, v)
}

func (t *invertedTheme) Font(style fyne.TextStyle) fyne.Resource { return theme.Current().Font(style) }

func (t *invertedTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.Current().Icon(name)
}

func (t *invertedTheme) Size(name fyne.ThemeSizeName) float32 { return theme.Current().Size(name) }
