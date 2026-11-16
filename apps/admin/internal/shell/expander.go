package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	ruleWidth    float32 = 2
	dotPitch     float32 = 4
	expanderTabX float32 = 3 * gridUnit / 2
)

type expander struct {
	widget.BaseWidget
	header  *widget.Button
	content fyne.CanvasObject
	open    bool
}

func newExpander(title string, content fyne.CanvasObject) *expander {
	e := &expander{content: content}
	e.header = widget.NewButtonWithIcon(title, theme.MenuExpandIcon(), e.toggle)
	e.header.Importance = widget.LowImportance
	e.header.Alignment = widget.ButtonAlignLeading
	content.Hide()
	e.ExtendBaseWidget(e)
	return e
}

func (e *expander) toggle() {
	e.open = !e.open
	if e.open {
		e.header.SetIcon(theme.MenuDropDownIcon())
		e.content.Show()
	} else {
		e.header.SetIcon(theme.MenuExpandIcon())
		e.content.Hide()
	}
	e.Refresh()
}

func (e *expander) CreateRenderer() fyne.WidgetRenderer {
	return &expanderRenderer{e: e, solid: canvas.NewRectangle(color.Transparent)}
}

type expanderRenderer struct {
	e     *expander
	solid *canvas.Rectangle
	dots  []*canvas.Rectangle
	size  fyne.Size
}

func (r *expanderRenderer) Objects() []fyne.CanvasObject {
	objects := []fyne.CanvasObject{r.e.header, r.e.content, r.solid}
	for _, d := range r.dots {
		objects = append(objects, d)
	}
	return objects
}

func (r *expanderRenderer) Destroy() {}

func (r *expanderRenderer) Layout(size fyne.Size) {
	r.size = size
	header := r.e.header.MinSize()
	r.e.header.Move(fyne.NewPos(expanderTabX, 0))
	r.e.header.Resize(clampSize(fyne.NewSize(size.Width-expanderTabX, header.Height)))
	r.e.content.Move(fyne.NewPos(expanderTabX, header.Height))
	r.e.content.Resize(clampSize(fyne.NewSize(size.Width-expanderTabX, size.Height-header.Height)))
	r.layoutRule(size.Height)
}

func (r *expanderRenderer) layoutRule(height float32) {
	strong := designColor(r.e.Theme(), colorNameBorderStrong, fyne.CurrentApp().Settings().ThemeVariant())
	r.solid.FillColor = strong
	r.solid.Move(fyne.NewPos(0, 0))
	r.solid.Resize(clampSize(fyne.NewSize(ruleWidth, height)))
	if r.e.open {
		r.solid.Show()
		r.dots = nil
		return
	}
	r.solid.Hide()
	count := int(height / dotPitch)
	r.dots = r.dots[:0]
	for i := 0; i < count; i++ {
		dot := canvas.NewRectangle(strong)
		dot.Move(fyne.NewPos(0, float32(i)*dotPitch))
		dot.Resize(clampSize(fyne.NewSquareSize(ruleWidth)))
		r.dots = append(r.dots, dot)
	}
}

func (r *expanderRenderer) MinSize() fyne.Size {
	min := r.e.header.MinSize()
	if r.e.open {
		content := r.e.content.MinSize()
		min = fyne.NewSize(max(min.Width, content.Width), min.Height+content.Height)
	}
	return min.AddWidthHeight(expanderTabX, 0)
}

func (r *expanderRenderer) Refresh() {
	r.Layout(r.e.Size())
	r.e.header.Refresh()
	r.e.content.Refresh()
	canvas.Refresh(r.e)
}
