package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const readableColumnEms = 52

func NewHeading(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.SizeName = theme.SizeNameHeadingText
	return label
}

func NewSectionTitle(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.SizeName = theme.SizeNameSubHeadingText
	return label
}

func NewFieldLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func NewMono(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
}

func NewKicker(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	label.SizeName = theme.SizeNameCaptionText
	return label
}

func NewPageHeader(kicker string, title fyne.CanvasObject, trailing ...fyne.CanvasObject) fyne.CanvasObject {
	left := container.NewVBox(title)
	if kicker != "" {
		left = container.NewVBox(NewKicker(kicker), title)
	}
	right := container.NewVBox(layout.NewSpacer(), container.NewHBox(trailing...))
	return container.NewVBox(
		container.NewBorder(nil, nil, left, right),
		widget.NewSeparator(),
		VerticalSpace(gridUnit),
	)
}

func VerticalSpace(height float32) fyne.CanvasObject {
	return FixedSpace(0, height)
}

func FixedSpace(width, height float32) fyne.CanvasObject {
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(width, height))
	return gap
}

func NewSectionGap() fyne.CanvasObject {
	return VerticalSpace(2 * gridUnit)
}

type insetLayout struct {
	top, right, bottom, left float32
}

func (l insetLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		if o.Visible() {
			size = size.Max(o.MinSize())
		}
	}
	return size.AddWidthHeight(l.left+l.right, l.top+l.bottom)
}

func (l insetLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(l.left, l.top))
		o.Resize(size.SubtractWidthHeight(l.left+l.right, l.top+l.bottom))
	}
}

func Inset(top, right, bottom, left float32, content fyne.CanvasObject) *fyne.Container {
	return container.New(insetLayout{top: top, right: right, bottom: bottom, left: left}, content)
}

func PageMargins(content fyne.CanvasObject) *fyne.Container {
	return Inset(contentMargin, contentMargin, contentMargin, contentMargin, content)
}

type columnLayout struct {
	maxWidth float32
}

func (l columnLayout) limit() float32 {
	if l.maxWidth > 0 {
		return l.maxWidth
	}
	return theme.TextSize() * readableColumnEms
}

func (l columnLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		if o.Visible() {
			size = size.Max(o.MinSize())
		}
	}
	return size
}

func (l columnLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := min(size.Width, l.limit())
	for _, o := range objects {
		width = max(width, o.MinSize().Width)
		o.Move(fyne.NewPos(0, 0))
		o.Resize(fyne.NewSize(min(width, size.Width), size.Height))
	}
}

func ReadableColumn(content fyne.CanvasObject) *fyne.Container {
	return container.New(columnLayout{}, content)
}

type centredColumnLayout struct {
	columnLayout
}

func (l centredColumnLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := min(size.Width, l.limit())
	for _, o := range objects {
		width = min(max(width, o.MinSize().Width), size.Width)
		o.Move(fyne.NewPos((size.Width-width)/2, 0))
		o.Resize(fyne.NewSize(width, size.Height))
	}
}

func CentredColumn(content fyne.CanvasObject) *fyne.Container {
	return container.New(centredColumnLayout{}, content)
}

func clampSize(size fyne.Size) fyne.Size {
	return fyne.NewSize(max(size.Width, 0), max(size.Height, 0))
}

type gutterGridLayout struct {
	columns int
	gutter  float32
}

func (l gutterGridLayout) rows(objects []fyne.CanvasObject) [][]fyne.CanvasObject {
	var rows [][]fyne.CanvasObject
	var row []fyne.CanvasObject
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		row = append(row, o)
		if len(row) == l.columns {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

func (l gutterGridLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var cell fyne.Size
	var height float32
	rows := l.rows(objects)
	for _, row := range rows {
		var rowHeight float32
		for _, o := range row {
			min := o.MinSize()
			cell.Width = max(cell.Width, min.Width)
			rowHeight = max(rowHeight, min.Height)
		}
		height += rowHeight
	}
	if len(rows) > 1 {
		height += l.gutter * float32(len(rows)-1)
	}
	width := cell.Width*float32(l.columns) + l.gutter*float32(l.columns-1)
	return fyne.NewSize(width, height)
}

func (l gutterGridLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := (size.Width - l.gutter*float32(l.columns-1)) / float32(l.columns)
	var y float32
	for _, row := range l.rows(objects) {
		var rowHeight float32
		for _, o := range row {
			rowHeight = max(rowHeight, o.MinSize().Height)
		}
		for i, o := range row {
			o.Move(fyne.NewPos(float32(i)*(width+l.gutter), y))
			o.Resize(clampSize(fyne.NewSize(width, rowHeight)))
		}
		y += rowHeight + l.gutter
	}
}

func NewCardGrid(columns int, objects ...fyne.CanvasObject) *fyne.Container {
	return container.New(gutterGridLayout{columns: columns, gutter: 2 * gridUnit}, objects...)
}

func ScrollBody(content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVScroll(Inset(0, 2*gridUnit, 2*gridUnit, 0, content))
}

type weightedRowLayout struct {
	weights []float32
}

func (l weightedRowLayout) weight(i int) float32 {
	if i < len(l.weights) {
		return l.weights[i]
	}
	return 1
}

func (l weightedRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		min := o.MinSize()
		size.Width += min.Width
		size.Height = max(size.Height, min.Height)
	}
	return size
}

func (l weightedRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var total float32
	for i := range objects {
		total += l.weight(i)
	}
	var x float32
	for i, o := range objects {
		width := size.Width * l.weight(i) / total
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(width, size.Height))
		x += width
	}
}

func NewWeightedRow(weights []float32, cells ...fyne.CanvasObject) *fyne.Container {
	return container.New(weightedRowLayout{weights: weights}, cells...)
}
