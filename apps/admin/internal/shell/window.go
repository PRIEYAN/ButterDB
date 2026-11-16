package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type bandMode int

const (
	bandNone bandMode = iota
	bandHeader
	bandFull
)

const (
	titleBlockTopPadding    = 1.5 * gridUnit
	titleBlockBottomPadding = gridUnit
	glyphGap                = 6
	glyphInset              = 10
)

type WindowCard struct {
	widget.BaseWidget
	Label   string
	Title   string
	Content fyne.CanvasObject

	padding float32
	band    bandMode
	title   *widget.Label
	holder  *fyne.Container
	heading fyne.CanvasObject
}

func NewWindowCard(label, title string, content fyne.CanvasObject) *WindowCard {
	return newWindowCard(label, title, content, cardPadding, bandNone)
}

func NewFlushWindowCard(label string, content fyne.CanvasObject) *WindowCard {
	return newWindowCard(label, "", content, 0, bandNone)
}

func NewHeaderBandCard(label, title string, content fyne.CanvasObject) *WindowCard {
	return newWindowCard(label, title, content, cardPadding, bandHeader)
}

func NewHighlightCard(label, title string, content fyne.CanvasObject) *WindowCard {
	return newWindowCard(label, title, content, cardPadding, bandFull)
}

func newWindowCard(label, title string, content fyne.CanvasObject, padding float32, band bandMode) *WindowCard {
	if content == nil {
		content = container.NewStack()
	}
	c := &WindowCard{Label: label, Title: title, Content: content, padding: padding, band: band}
	c.title = NewSectionTitle(title)
	c.title.Wrapping = fyne.TextWrapWord
	c.heading = c.title
	if band != bandNone {
		c.heading = container.NewThemeOverride(c.title, onAccentTheme{})
	}
	c.holder = container.NewStack(content)
	if band == bandFull {
		c.holder = container.NewStack(container.NewThemeOverride(content, onAccentTheme{}))
	}
	c.ExtendBaseWidget(c)
	return c
}

func (c *WindowCard) SetLabel(label string) {
	if c.Label == label {
		return
	}
	c.Label = label
	c.Refresh()
}

func (c *WindowCard) SetTitle(title string) {
	c.Title = title
	c.title.SetText(title)
	c.Refresh()
}

func (c *WindowCard) CreateRenderer() fyne.WidgetRenderer {
	r := &windowCardRenderer{
		card:       c,
		shadow:     canvas.NewRectangle(color.Transparent),
		box:        newBorderBox(),
		titleBar:   canvas.NewRectangle(color.Transparent),
		titleRule:  canvas.NewRectangle(color.Transparent),
		headerFill: canvas.NewRectangle(color.Transparent),
		headerRule: canvas.NewRectangle(color.Transparent),
		square:     canvas.NewRectangle(color.Transparent),
		circle:     canvas.NewCircle(color.Transparent),
		dot:        canvas.NewCircle(color.Transparent),
		label:      canvas.NewText("", color.Transparent),
	}
	r.label.TextStyle = fyne.TextStyle{Monospace: true}
	r.applyTheme()
	return r
}

type windowCardRenderer struct {
	card       *WindowCard
	shadow     *canvas.Rectangle
	box        *borderBox
	titleBar   *canvas.Rectangle
	titleRule  *canvas.Rectangle
	headerFill *canvas.Rectangle
	headerRule *canvas.Rectangle
	square     *canvas.Rectangle
	circle     *canvas.Circle
	dot        *canvas.Circle
	label      *canvas.Text
}

func (r *windowCardRenderer) hasTitle() bool {
	return r.card.Title != ""
}

func (r *windowCardRenderer) Objects() []fyne.CanvasObject {
	objects := append([]fyne.CanvasObject{r.shadow}, r.box.objects()...)
	objects = append(objects, r.headerFill, r.titleBar, r.titleRule, r.headerRule, r.square, r.circle, r.dot, r.label)
	if r.hasTitle() {
		objects = append(objects, r.card.heading)
	}
	return append(objects, r.card.holder)
}

func (r *windowCardRenderer) Destroy() {}

func (r *windowCardRenderer) titleBlockHeight(width float32) float32 {
	if !r.hasTitle() {
		return 0
	}
	r.card.heading.Resize(clampSize(fyne.NewSize(width, r.card.heading.MinSize().Height)))
	return titleBlockTopPadding + r.card.heading.MinSize().Height + titleBlockBottomPadding
}

func (r *windowCardRenderer) Layout(size fyne.Size) {
	inner := size.SubtractWidthHeight(shadowOffset, shadowOffset)
	r.shadow.Move(fyne.NewPos(shadowOffset, shadowOffset))
	r.shadow.Resize(clampSize(inner))
	r.box.place(fyne.NewPos(0, 0), inner)
	r.titleBar.Move(fyne.NewPos(borderWidth, borderWidth))
	r.titleBar.Resize(clampSize(fyne.NewSize(inner.Width-2*borderWidth, titleBarHeight-borderWidth)))
	r.titleRule.Move(fyne.NewPos(0, titleBarHeight))
	r.titleRule.Resize(clampSize(fyne.NewSize(inner.Width, borderWidth)))
	r.layoutGlyphs()
	textWidth := inner.Width - 2*r.card.padding
	if r.card.padding == 0 {
		textWidth = inner.Width - 2*cardPadding
	}
	headerTop := titleBarHeight + borderWidth
	blockHeight := r.titleBlockHeight(textWidth)
	r.layoutHeader(inner, headerTop, blockHeight)
	contentTop := headerTop + blockHeight + r.contentTopPadding()
	r.card.holder.Move(fyne.NewPos(r.card.padding+r.edgeInset(), contentTop))
	r.card.holder.Resize(clampSize(fyne.NewSize(inner.Width-2*(r.card.padding+r.edgeInset()), inner.Height-contentTop-r.card.padding-r.edgeInset())))
}

func (r *windowCardRenderer) edgeInset() float32 {
	if r.card.padding == 0 {
		return borderWidth
	}
	return 0
}

func (r *windowCardRenderer) contentTopPadding() float32 {
	if r.hasTitle() {
		return gridUnit
	}
	return r.card.padding
}

func (r *windowCardRenderer) layoutHeader(inner fyne.Size, top, height float32) {
	r.headerFill.Move(fyne.NewPos(borderWidth, top))
	r.headerFill.Resize(clampSize(fyne.NewSize(inner.Width-2*borderWidth, height)))
	r.headerRule.Move(fyne.NewPos(0, top+height))
	r.headerRule.Resize(clampSize(fyne.NewSize(inner.Width, borderWidth)))
	if !r.hasTitle() {
		return
	}
	inset := r.card.padding
	if inset == 0 {
		inset = cardPadding
	}
	r.card.heading.Move(fyne.NewPos(inset, top+titleBlockTopPadding))
}

func (r *windowCardRenderer) layoutGlyphs() {
	y := (titleBarHeight - glyphSize) / 2
	x := float32(glyphInset)
	for _, glyph := range []fyne.CanvasObject{r.square, r.circle, r.dot} {
		glyph.Move(fyne.NewPos(x, y))
		glyph.Resize(clampSize(fyne.NewSquareSize(glyphSize)))
		x += glyphSize + glyphGap
	}
	labelSize := r.label.MinSize()
	r.label.Move(fyne.NewPos(x+glyphGap, (titleBarHeight-labelSize.Height)/2))
	r.label.Resize(clampSize(labelSize))
}

func (r *windowCardRenderer) MinSize() fyne.Size {
	content := r.card.holder.MinSize()
	width := content.Width + 2*(r.card.padding+r.edgeInset())
	height := titleBarHeight + borderWidth + content.Height + r.contentTopPadding() + r.card.padding + r.edgeInset()
	if r.hasTitle() {
		heading := r.card.heading.MinSize()
		height += titleBlockTopPadding + heading.Height + titleBlockBottomPadding
		width = max(width, heading.Width+2*cardPadding)
	}
	chrome := glyphInset + 3*(glyphSize+glyphGap) + glyphGap + r.label.MinSize().Width + glyphInset
	width = max(width, chrome)
	return fyne.NewSize(width+shadowOffset, height+shadowOffset)
}

func (r *windowCardRenderer) Refresh() {
	r.applyTheme()
	r.Layout(r.card.Size())
	canvas.Refresh(r.card)
}

func (r *windowCardRenderer) applyTheme() {
	th := r.card.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	strong := designColor(th, colorNameBorderStrong, v)
	surface := designColor(th, colorNameSurface, v)
	accent := designColor(th, colorNameAccent, v)
	r.shadow.FillColor = designColor(th, colorNameSolidShadow, v)
	edge, fill := strong, surface
	r.titleBar.FillColor = designColor(th, colorNameTitleBar, v)
	r.titleRule.FillColor = strong
	r.headerFill.FillColor = color.Transparent
	r.headerRule.FillColor = color.Transparent
	r.label.Color = th.Color(theme.ColorNameForeground, v)
	r.label.Text = r.card.Label
	r.label.TextSize = th.Size(theme.SizeNameCaptionText)
	r.square.StrokeColor, r.square.StrokeWidth, r.square.FillColor = strong, borderWidth, color.Transparent
	r.circle.StrokeColor, r.circle.StrokeWidth, r.circle.FillColor = strong, borderWidth, color.Transparent
	r.dot.StrokeColor, r.dot.StrokeWidth, r.dot.FillColor = strong, borderWidth, accent
	edge, fill = r.applyBand(th, v, accent, edge, fill)
	r.box.setColors(edge, fill)
	for _, o := range []fyne.CanvasObject{r.shadow, r.titleBar, r.titleRule, r.headerFill, r.headerRule, r.square, r.circle, r.dot, r.label} {
		o.Refresh()
	}
	r.card.heading.Refresh()
	r.card.holder.Refresh()
}

func (r *windowCardRenderer) applyBand(th fyne.Theme, v fyne.ThemeVariant, accent, edge, fill color.Color) (color.Color, color.Color) {
	onAccent := designColor(th, colorNameOnAccent, v)
	switch r.card.band {
	case bandFull:
		r.titleBar.FillColor = accent
		r.label.Color = onAccent
		r.dot.FillColor = onAccent
		r.titleRule.FillColor = onAccent
		r.square.StrokeColor, r.circle.StrokeColor, r.dot.StrokeColor = onAccent, onAccent, onAccent
		return onAccent, accent
	case bandHeader:
		r.titleBar.FillColor = accent
		r.headerFill.FillColor = accent
		r.label.Color = onAccent
		r.dot.FillColor = onAccent
		r.square.StrokeColor, r.circle.StrokeColor, r.dot.StrokeColor = onAccent, onAccent, onAccent
		if r.hasTitle() {
			r.headerRule.FillColor = edge
		}
	}
	return edge, fill
}

type onAccentTheme struct{}

func (onAccentTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	base := theme.Current()
	switch name {
	case theme.ColorNameForeground, theme.ColorNameSuccess, theme.ColorNameWarning, theme.ColorNameError,
		theme.ColorNameHyperlink, theme.ColorNamePrimary, theme.ColorNameInputBorder, colorNameBorderStrong:
		return designColor(base, colorNameOnAccent, v)
	case theme.ColorNameForegroundOnPrimary:
		return designColor(base, colorNameAccent, v)
	}
	return base.Color(name, v)
}

func OnAccentIcon(res fyne.Resource) fyne.Resource {
	return theme.NewColoredResource(res, colorNameOnAccent)
}

func (onAccentTheme) Font(style fyne.TextStyle) fyne.Resource { return theme.Current().Font(style) }

func (onAccentTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return theme.Current().Icon(name) }

func (onAccentTheme) Size(name fyne.ThemeSizeName) float32 { return theme.Current().Size(name) }
