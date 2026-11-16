package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

const codeBlockPadding float32 = 1.5 * gridUnit

type CodeBlock struct {
	widget.BaseWidget
	Content fyne.CanvasObject
}

func NewCodeBlock(content fyne.CanvasObject) *CodeBlock {
	b := &CodeBlock{Content: content}
	b.ExtendBaseWidget(b)
	return b
}

func (b *CodeBlock) Visible() bool {
	return b.BaseWidget.Visible() && b.Content.Visible()
}

func (b *CodeBlock) CreateRenderer() fyne.WidgetRenderer {
	r := &codeBlockRenderer{block: b, frame: newBorderBox()}
	r.applyTheme()
	return r
}

type codeBlockRenderer struct {
	block *CodeBlock
	frame *borderBox
}

func (r *codeBlockRenderer) Objects() []fyne.CanvasObject {
	return append(r.frame.objects(), r.block.Content)
}

func (r *codeBlockRenderer) Destroy() {}

func (r *codeBlockRenderer) Layout(size fyne.Size) {
	r.frame.place(fyne.NewPos(0, 0), size)
	r.block.Content.Move(fyne.NewPos(codeBlockPadding/2, codeBlockPadding/4))
	r.block.Content.Resize(clampSize(size.SubtractWidthHeight(codeBlockPadding, codeBlockPadding/2)))
}

func (r *codeBlockRenderer) MinSize() fyne.Size {
	return r.block.Content.MinSize().AddWidthHeight(codeBlockPadding, codeBlockPadding/2)
}

func (r *codeBlockRenderer) Refresh() {
	r.applyTheme()
	r.block.Content.Refresh()
}

func (r *codeBlockRenderer) applyTheme() {
	th := r.block.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	r.frame.setColors(designColor(th, colorNameBorderStrong, v), designColor(th, colorNameTitleBar, v))
}
