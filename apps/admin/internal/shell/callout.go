package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type CalloutKind int

const (
	CalloutInfo CalloutKind = iota
	CalloutWarning
	CalloutDanger
	CalloutSuccess
)

const (
	calloutBarWidth float32 = 4
	calloutPadding  float32 = 1.5 * gridUnit
	calloutIconTop  float32 = gridUnit
)

type Callout struct {
	widget.BaseWidget
	Kind    CalloutKind
	Content fyne.CanvasObject
	icon    *widget.Icon
}

func NewCallout(kind CalloutKind, content fyne.CanvasObject) *Callout {
	c := &Callout{Kind: kind, Content: content, icon: widget.NewIcon(calloutIcon(kind))}
	c.ExtendBaseWidget(c)
	return c
}

func NewCalloutText(kind CalloutKind, text string) *Callout {
	return NewCallout(kind, newWrappedLabel(text))
}

func (c *Callout) Visible() bool {
	return c.BaseWidget.Visible() && c.Content.Visible()
}

func (c *Callout) SetKind(kind CalloutKind) {
	c.Kind = kind
	c.icon.SetResource(calloutIcon(kind))
	c.Refresh()
}

func calloutIcon(kind CalloutKind) fyne.Resource {
	switch kind {
	case CalloutWarning:
		return theme.NewWarningThemedResource(theme.WarningIcon())
	case CalloutDanger:
		return theme.NewErrorThemedResource(theme.ErrorIcon())
	case CalloutSuccess:
		return theme.NewSuccessThemedResource(theme.ConfirmIcon())
	}
	return theme.NewThemedResource(theme.InfoIcon())
}

func calloutColorName(kind CalloutKind) fyne.ThemeColorName {
	switch kind {
	case CalloutWarning:
		return theme.ColorNameWarning
	case CalloutDanger:
		return theme.ColorNameError
	case CalloutSuccess:
		return theme.ColorNameSuccess
	}
	return colorNameInfo
}

func (c *Callout) CreateRenderer() fyne.WidgetRenderer {
	r := &calloutRenderer{
		callout: c,
		frame:   newBorderBox(),
		bar:     canvas.NewRectangle(color.Transparent),
		body:    container.NewBorder(nil, nil, Inset(calloutIconTop, gridUnit/2, 0, 0, container.NewVBox(c.icon)), nil, c.Content),
	}
	r.applyTheme()
	return r
}

type calloutRenderer struct {
	callout *Callout
	frame   *borderBox
	bar     *canvas.Rectangle
	body    *fyne.Container
}

func (r *calloutRenderer) Objects() []fyne.CanvasObject {
	return append(r.frame.objects(), r.bar, r.body)
}

func (r *calloutRenderer) Destroy() {}

func (r *calloutRenderer) Layout(size fyne.Size) {
	r.frame.place(fyne.NewPos(0, 0), size)
	r.bar.Move(fyne.NewPos(0, 0))
	r.bar.Resize(clampSize(fyne.NewSize(calloutBarWidth, size.Height)))
	left := calloutBarWidth + calloutPadding/2
	r.body.Move(fyne.NewPos(left, calloutPadding/4))
	r.body.Resize(clampSize(fyne.NewSize(size.Width-left-calloutPadding/2, size.Height-calloutPadding/2)))
}

func (r *calloutRenderer) MinSize() fyne.Size {
	return r.body.MinSize().AddWidthHeight(calloutBarWidth+calloutPadding, calloutPadding/2)
}

func (r *calloutRenderer) Refresh() {
	r.applyTheme()
	r.bar.Refresh()
	r.body.Refresh()
}

func (r *calloutRenderer) applyTheme() {
	th := r.callout.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	r.frame.setColors(designColor(th, colorNameBorderStrong, v), designColor(th, colorNameSurface, v))
	r.bar.FillColor = designColor(th, calloutColorName(r.callout.Kind), v)
}
