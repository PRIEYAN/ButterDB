package shell

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	badgePadX      float32 = 6
	badgePadY      float32 = 3
	badgeKeyGap    float32 = 4
	badgeKeySuffix         = ":"
	badgeJoiner            = " | "
)

type BadgePart struct {
	Key   string
	Value string
}

type Badge struct {
	widget.BaseWidget
	Parts []BadgePart
}

func NewBadge(parts ...BadgePart) *Badge {
	b := &Badge{Parts: parts}
	b.ExtendBaseWidget(b)
	return b
}

func NewValueBadge(value string) *Badge {
	return NewBadge(BadgePart{Value: value})
}

func (b *Badge) SetParts(parts ...BadgePart) {
	b.Parts = parts
	b.Refresh()
}

func (b *Badge) Text() string {
	pieces := make([]string, 0, len(b.Parts))
	for _, p := range b.Parts {
		if p.Key == "" {
			pieces = append(pieces, p.Value)
			continue
		}
		pieces = append(pieces, strings.ToUpper(p.Key)+badgeKeySuffix+" "+p.Value)
	}
	return strings.Join(pieces, badgeJoiner)
}

func (b *Badge) CreateRenderer() fyne.WidgetRenderer {
	r := &badgeRenderer{badge: b, frame: newBorderBox()}
	r.rebuild()
	return r
}

type badgeRenderer struct {
	badge  *Badge
	frame  *borderBox
	keys   []*canvas.Text
	values []*canvas.Text
	rules  []*canvas.Rectangle
}

func (r *badgeRenderer) rebuild() {
	r.keys, r.values, r.rules = nil, nil, nil
	for i, p := range r.badge.Parts {
		key := canvas.NewText("", color.Transparent)
		if p.Key != "" {
			key.Text = strings.ToUpper(p.Key) + badgeKeySuffix
		}
		value := canvas.NewText(p.Value, color.Transparent)
		key.TextStyle = fyne.TextStyle{Monospace: true}
		value.TextStyle = fyne.TextStyle{Monospace: true}
		r.keys = append(r.keys, key)
		r.values = append(r.values, value)
		if i > 0 {
			r.rules = append(r.rules, canvas.NewRectangle(color.Transparent))
		}
	}
	r.applyTheme()
}

func (r *badgeRenderer) applyTheme() {
	th := r.badge.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	strong := designColor(th, colorNameBorderStrong, v)
	size := th.Size(theme.SizeNameCaptionText)
	r.frame.setColors(strong, designColor(th, colorNameSurface, v))
	for i := range r.keys {
		r.keys[i].Color = designColor(th, colorNameInkSecondary, v)
		r.keys[i].TextSize = size
		r.values[i].Color = th.Color(theme.ColorNameForeground, v)
		r.values[i].TextSize = size
	}
	for _, rule := range r.rules {
		rule.FillColor = strong
	}
}

func (r *badgeRenderer) Objects() []fyne.CanvasObject {
	objects := r.frame.objects()
	for i := range r.keys {
		objects = append(objects, r.keys[i], r.values[i])
	}
	for _, rule := range r.rules {
		objects = append(objects, rule)
	}
	return objects
}

func (r *badgeRenderer) Destroy() {}

func (r *badgeRenderer) partWidth(i int) float32 {
	width := r.values[i].MinSize().Width
	if r.keys[i].Text != "" {
		width += r.keys[i].MinSize().Width + badgeKeyGap
	}
	return width
}

func (r *badgeRenderer) textHeight() float32 {
	var height float32
	for i := range r.values {
		height = max(height, r.values[i].MinSize().Height)
	}
	if height == 0 {
		height = canvas.NewText("M", color.Transparent).MinSize().Height
	}
	return height
}

func (r *badgeRenderer) MinSize() fyne.Size {
	width := badgePadX * 2
	for i := range r.values {
		width += r.partWidth(i)
		if i > 0 {
			width += 2*badgePadX + borderWidth
		}
	}
	return fyne.NewSize(width, r.textHeight()+2*badgePadY)
}

func (r *badgeRenderer) Layout(size fyne.Size) {
	min := r.MinSize()
	r.frame.place(fyne.NewPos(0, (size.Height-min.Height)/2), min)
	top := (size.Height-min.Height)/2 + badgePadY
	x := badgePadX
	for i := range r.values {
		if i > 0 {
			r.rules[i-1].Move(fyne.NewPos(x+badgePadX, (size.Height-min.Height)/2))
			r.rules[i-1].Resize(clampSize(fyne.NewSize(borderWidth, min.Height)))
			x += 2*badgePadX + borderWidth
		}
		if r.keys[i].Text != "" {
			r.keys[i].Move(fyne.NewPos(x, top))
			r.keys[i].Resize(clampSize(r.keys[i].MinSize()))
			x += r.keys[i].MinSize().Width + badgeKeyGap
		}
		r.values[i].Move(fyne.NewPos(x, top))
		r.values[i].Resize(clampSize(r.values[i].MinSize()))
		x += r.values[i].MinSize().Width
	}
}

func (r *badgeRenderer) Refresh() {
	if len(r.values) != len(r.badge.Parts) || r.textChanged() {
		r.rebuild()
	}
	r.applyTheme()
	r.Layout(r.badge.Size())
	canvas.Refresh(r.badge)
}

func (r *badgeRenderer) textChanged() bool {
	for i, p := range r.badge.Parts {
		if r.values[i].Text != p.Value {
			return true
		}
		key := ""
		if p.Key != "" {
			key = strings.ToUpper(p.Key) + badgeKeySuffix
		}
		if r.keys[i].Text != key {
			return true
		}
	}
	return false
}
