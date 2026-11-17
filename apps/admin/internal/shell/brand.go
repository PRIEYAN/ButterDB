package shell

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

//go:embed brand.png
var brandPNG []byte

func newBrandPanel() fyne.CanvasObject {
	logo := canvas.NewImageFromResource(fyne.NewStaticResource("brand.png", brandPNG))
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(256, 256))
	return container.NewCenter(logo)
}

func newSplitView(brand, main fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWithColumns(2, brand, main)
}
