package shell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

const meterHeight float32 = 10

type meterBar struct {
	widget.BaseWidget
	fraction float64
}

func NewMeter(fraction float64, text string) fyne.CanvasObject {
	return container.NewVBox(NewMeterBar(fraction), NewMono(text))
}

func NewMeterBar(fraction float64) fyne.CanvasObject {
	bar := &meterBar{fraction: min(max(fraction, 0), 1)}
	bar.ExtendBaseWidget(bar)
	return bar
}

func (m *meterBar) CreateRenderer() fyne.WidgetRenderer {
	r := &meterRenderer{bar: m, track: newBorderBox(), fill: canvas.NewRectangle(color.Transparent)}
	r.applyTheme()
	return r
}

type meterRenderer struct {
	bar   *meterBar
	track *borderBox
	fill  *canvas.Rectangle
}

func (r *meterRenderer) Objects() []fyne.CanvasObject { return append(r.track.objects(), r.fill) }

func (r *meterRenderer) Destroy() {}

func (r *meterRenderer) Layout(size fyne.Size) {
	top := (size.Height - meterHeight) / 2
	r.track.place(fyne.NewPos(0, top), fyne.NewSize(size.Width, meterHeight))
	inner := size.Width - 2*borderWidth
	r.fill.Move(fyne.NewPos(borderWidth, top+borderWidth))
	r.fill.Resize(clampSize(fyne.NewSize(inner*float32(r.bar.fraction), meterHeight-2*borderWidth)))
}

func (r *meterRenderer) MinSize() fyne.Size {
	return fyne.NewSize(4*gridUnit, meterHeight+gridUnit)
}

func (r *meterRenderer) Refresh() {
	r.applyTheme()
	r.Layout(r.bar.Size())
	canvas.Refresh(r.bar)
}

func (r *meterRenderer) applyTheme() {
	th := r.bar.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	r.track.setColors(designColor(th, colorNameBorderStrong, v), designColor(th, colorNameSurface, v))
	r.fill.FillColor = designColor(th, colorNameAccent, v)
	r.fill.Refresh()
}
