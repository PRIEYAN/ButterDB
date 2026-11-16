package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
)

type borderBox struct {
	edge *canvas.Rectangle
	fill *canvas.Rectangle
}

func newBorderBox() *borderBox {
	return &borderBox{edge: canvas.NewRectangle(color.Transparent), fill: canvas.NewRectangle(color.Transparent)}
}

func (b *borderBox) objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{b.edge, b.fill}
}

func (b *borderBox) setColors(edge, fill color.Color) {
	b.edge.FillColor = edge
	b.fill.FillColor = fill
	b.edge.Refresh()
	b.fill.Refresh()
}

func (b *borderBox) place(pos fyne.Position, size fyne.Size) {
	b.edge.Move(pos)
	b.edge.Resize(clampSize(size))
	b.fill.Move(pos.AddXY(borderWidth, borderWidth))
	b.fill.Resize(clampSize(size.SubtractWidthHeight(2*borderWidth, 2*borderWidth)))
}

type outline struct {
	sides [4]*canvas.Rectangle
}

func newOutline() *outline {
	o := &outline{}
	for i := range o.sides {
		o.sides[i] = canvas.NewRectangle(color.Transparent)
	}
	return o
}

func (o *outline) objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{o.sides[0], o.sides[1], o.sides[2], o.sides[3]}
}

func (o *outline) setColor(c color.Color) {
	for _, side := range o.sides {
		side.FillColor = c
		side.Refresh()
	}
}

func (o *outline) place(size fyne.Size) {
	o.sides[0].Move(fyne.NewPos(0, 0))
	o.sides[0].Resize(clampSize(fyne.NewSize(size.Width, borderWidth)))
	o.sides[1].Move(fyne.NewPos(0, size.Height-borderWidth))
	o.sides[1].Resize(clampSize(fyne.NewSize(size.Width, borderWidth)))
	o.sides[2].Move(fyne.NewPos(0, 0))
	o.sides[2].Resize(clampSize(fyne.NewSize(borderWidth, size.Height)))
	o.sides[3].Move(fyne.NewPos(size.Width-borderWidth, 0))
	o.sides[3].Resize(clampSize(fyne.NewSize(borderWidth, size.Height)))
}
