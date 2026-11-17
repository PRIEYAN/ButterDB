package shell

import (
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	pickerScale     = 0.85
	pickerMinWidth  = 960
	pickerMinHeight = 640
	pickerItemW     = 112
	pickerItemH     = 132
	pickerSideWidth = 200
)

var pickerLastDir string

type pickerEntry struct {
	name string
	path string
	dir  bool
}

type filePicker struct {
	widget.BaseWidget
	win      fyne.Window
	popup    *widget.PopUp
	dir      string
	entries  []pickerEntry
	selected map[int]bool
	anchor   int
	grid     *widget.GridWrap
	crumbs   *fyne.Container
	scroll   *container.Scroll
	status   *widget.Label
	open     *widget.Button
	content  fyne.CanvasObject
	onChosen func([]string)
}

func ShowFilesOpen(win fyne.Window, title string, onChosen func(paths []string)) {
	p := newFilePicker(win, title, onChosen)
	p.popup = widget.NewModalPopUp(newFileDialogFrame(p), win.Canvas())
	p.popup.Resize(pickerSize(win.Canvas().Size()))
	p.load(pickerStartDir())
	p.popup.Show()
	win.Canvas().Focus(p)
}

func pickerSize(canvasSize fyne.Size) fyne.Size {
	return fyne.NewSize(
		min(max(canvasSize.Width*pickerScale, pickerMinWidth), canvasSize.Width),
		min(max(canvasSize.Height*pickerScale, pickerMinHeight), canvasSize.Height),
	)
}

func pickerStartDir() string {
	if pickerLastDir != "" {
		if info, err := os.Stat(pickerLastDir); err == nil && info.IsDir() {
			return pickerLastDir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return string(filepath.Separator)
}

func newFilePicker(win fyne.Window, title string, onChosen func([]string)) *filePicker {
	p := &filePicker{win: win, onChosen: onChosen, selected: map[int]bool{}, anchor: -1}
	p.ExtendBaseWidget(p)
	p.grid = widget.NewGridWrap(
		func() int { return len(p.entries) },
		func() fyne.CanvasObject { return newPickerItem(p) },
		func(id widget.GridWrapItemID, obj fyne.CanvasObject) { obj.(*pickerItem).bind(id) },
	)
	p.crumbs = container.NewHBox()
	p.scroll = container.NewHScroll(p.crumbs)
	p.status = widget.NewLabel("")
	p.status.Truncation = fyne.TextTruncateEllipsis
	cancel := widget.NewButton("Cancel", p.cancel)
	p.open = widget.NewButton("Open", p.confirm)
	p.open.Importance = widget.HighImportance
	p.open.Disable()

	heading := NewHeading(title)
	hint := widget.NewLabel("Ctrl+click to pick several files · Ctrl+A selects every file")
	hint.Importance = widget.LowImportance
	top := container.NewVBox(heading, p.scroll)
	footer := container.NewBorder(nil, nil, nil, container.NewHBox(cancel, p.open), container.NewVBox(p.status, hint))
	side := container.NewVScroll(p.places())
	side.SetMinSize(fyne.NewSize(pickerSideWidth, 0))
	body := container.NewBorder(nil, nil, container.NewHBox(side, widget.NewSeparator()), nil, p.grid)
	p.content = container.NewBorder(top, footer, nil, nil, body)
	return p
}

func (p *filePicker) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(p.content)
}

func (p *filePicker) places() fyne.CanvasObject {
	home, _ := os.UserHomeDir()
	type place struct {
		name string
		icon fyne.Resource
		path string
	}
	list := []place{{"Home", theme.HomeIcon(), home}}
	for _, sub := range []place{
		{"Desktop", theme.DesktopIcon(), "Desktop"},
		{"Documents", theme.DocumentIcon(), "Documents"},
		{"Downloads", theme.DownloadIcon(), "Downloads"},
		{"Music", theme.MediaMusicIcon(), "Music"},
		{"Pictures", theme.MediaPhotoIcon(), "Pictures"},
		{"Videos", theme.MediaVideoIcon(), "Videos"},
	} {
		full := filepath.Join(home, sub.path)
		if info, err := os.Stat(full); home != "" && err == nil && info.IsDir() {
			list = append(list, place{sub.name, sub.icon, full})
		}
	}
	list = append(list, place{"Computer", theme.ComputerIcon(), string(filepath.Separator)})
	box := container.NewVBox()
	for _, pl := range list {
		target := pl.path
		btn := widget.NewButtonWithIcon(pl.name, pl.icon, func() { p.load(target) })
		btn.Alignment = widget.ButtonAlignLeading
		btn.Importance = widget.LowImportance
		box.Add(btn)
	}
	return box
}

func (p *filePicker) load(dir string) {
	items, err := os.ReadDir(dir)
	if err != nil {
		p.status.SetText("Cannot open " + dir)
		return
	}
	entries := make([]pickerEntry, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, item.Name())
		isDir := item.IsDir()
		if item.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(full); err == nil {
				isDir = info.IsDir()
			}
		}
		entries = append(entries, pickerEntry{name: item.Name(), path: full, dir: isDir})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].dir != entries[j].dir {
			return entries[i].dir
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})
	if parent := filepath.Dir(dir); parent != dir {
		entries = append([]pickerEntry{{name: "(Parent)", path: parent, dir: true}}, entries...)
	}
	p.dir = dir
	pickerLastDir = dir
	p.entries = entries
	p.selected = map[int]bool{}
	p.anchor = -1
	p.grid.Refresh()
	p.grid.ScrollToTop()
	p.buildCrumbs()
	p.updateFooter()
}

func (p *filePicker) buildCrumbs() {
	p.crumbs.RemoveAll()
	parts := []string{string(filepath.Separator)}
	for _, part := range strings.Split(strings.Trim(p.dir, string(filepath.Separator)), string(filepath.Separator)) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	path := ""
	for i, part := range parts {
		if i == 0 {
			path = part
		} else {
			path = filepath.Join(path, part)
		}
		target := path
		btn := widget.NewButton(part, func() { p.load(target) })
		if i == len(parts)-1 {
			btn.Importance = widget.HighImportance
		}
		p.crumbs.Add(btn)
	}
	p.scroll.ScrollToOffset(fyne.NewPos(p.crumbs.MinSize().Width, 0))
}

func (p *filePicker) click(i int, mods fyne.KeyModifier) {
	if i < 0 || i >= len(p.entries) {
		return
	}
	e := p.entries[i]
	ctrl := mods&(fyne.KeyModifierControl|fyne.KeyModifierSuper) != 0
	shift := mods&fyne.KeyModifierShift != 0
	switch {
	case e.dir && !ctrl && !shift:
		p.load(e.path)
		return
	case e.dir:
		return
	case shift && p.anchor >= 0:
		if !ctrl {
			p.selected = map[int]bool{}
		}
		lo, hi := min(p.anchor, i), max(p.anchor, i)
		for j := lo; j <= hi; j++ {
			if !p.entries[j].dir {
				p.selected[j] = true
			}
		}
	case ctrl:
		if p.selected[i] {
			delete(p.selected, i)
		} else {
			p.selected[i] = true
		}
		p.anchor = i
	default:
		p.selected = map[int]bool{i: true}
		p.anchor = i
	}
	p.grid.Refresh()
	p.updateFooter()
}

func (p *filePicker) activate(i int) {
	if i < 0 || i >= len(p.entries) {
		return
	}
	if p.entries[i].dir {
		p.load(p.entries[i].path)
		return
	}
	p.selected = map[int]bool{i: true}
	p.confirm()
}

func (p *filePicker) selectAll() {
	p.selected = map[int]bool{}
	for i, e := range p.entries {
		if !e.dir {
			p.selected[i] = true
		}
	}
	p.grid.Refresh()
	p.updateFooter()
}

func (p *filePicker) chosen() []string {
	paths := make([]string, 0, len(p.selected))
	for i, e := range p.entries {
		if p.selected[i] {
			paths = append(paths, e.path)
		}
	}
	return paths
}

func (p *filePicker) updateFooter() {
	paths := p.chosen()
	switch len(paths) {
	case 0:
		p.status.SetText("")
		p.open.Disable()
		p.open.SetText("Open")
	case 1:
		p.status.SetText(filepath.Base(paths[0]))
		p.open.Enable()
		p.open.SetText("Open")
	default:
		p.status.SetText(strconv.Itoa(len(paths)) + " files selected")
		p.open.Enable()
		p.open.SetText("Open " + strconv.Itoa(len(paths)))
	}
}

func (p *filePicker) confirm() {
	paths := p.chosen()
	if len(paths) == 0 {
		return
	}
	p.close()
	if p.onChosen != nil {
		p.onChosen(paths)
	}
}

func (p *filePicker) cancel() { p.close() }

func (p *filePicker) close() {
	if p.popup != nil {
		p.popup.Hide()
	}
}

func (p *filePicker) FocusGained()   {}
func (p *filePicker) FocusLost()     {}
func (p *filePicker) TypedRune(rune) {}
func (p *filePicker) TypedKey(e *fyne.KeyEvent) {
	switch e.Name {
	case fyne.KeyEscape:
		p.cancel()
	case fyne.KeyReturn, fyne.KeyEnter:
		p.confirm()
	}
}

func (p *filePicker) TypedShortcut(s fyne.Shortcut) {
	switch sc := s.(type) {
	case *fyne.ShortcutSelectAll:
		p.selectAll()
	case *desktop.CustomShortcut:
		if sc.KeyName == fyne.KeyA && sc.Modifier&(fyne.KeyModifierControl|fyne.KeyModifierSuper) != 0 {
			p.selectAll()
		}
	}
}

type pickerItem struct {
	widget.BaseWidget
	p     *filePicker
	index int
	bg    *canvas.Rectangle
	icon  *widget.FileIcon
	label *widget.RichText
	mods  fyne.KeyModifier
}

func newPickerItem(p *filePicker) *pickerItem {
	it := &pickerItem{p: p, index: -1, bg: canvas.NewRectangle(theme.Color(theme.ColorNamePrimary))}
	it.bg.Hide()
	it.icon = widget.NewFileIcon(nil)
	it.label = widget.NewRichText()
	it.label.Wrapping = fyne.TextWrapBreak
	it.label.Truncation = fyne.TextTruncateEllipsis
	it.ExtendBaseWidget(it)
	return it
}

func (it *pickerItem) bind(i int) {
	it.index = i
	e := it.p.entries[i]
	it.icon.SetURI(storage.NewFileURI(e.path))
	selected := it.p.selected[i]
	it.icon.SetSelected(selected)
	colorName := fyne.ThemeColorName(theme.ColorNameForeground)
	if selected {
		colorName = theme.ColorNameForegroundOnPrimary
	}
	it.label.Segments = []widget.RichTextSegment{&widget.TextSegment{
		Text:  e.name,
		Style: widget.RichTextStyle{Alignment: fyne.TextAlignCenter, ColorName: colorName, Inline: true},
	}}
	if selected {
		it.bg.FillColor = theme.Color(theme.ColorNamePrimary)
		it.bg.Show()
	} else {
		it.bg.Hide()
	}
	it.label.Refresh()
	it.bg.Refresh()
}

func (it *pickerItem) CreateRenderer() fyne.WidgetRenderer {
	iconBox := container.NewThemeOverride(container.NewGridWrap(fyne.NewSquareSize(64), it.icon), tileSelectionTheme{})
	body := container.NewBorder(container.NewCenter(iconBox), nil, nil, nil, it.label)
	return widget.NewSimpleRenderer(container.NewStack(it.bg, body))
}

func (it *pickerItem) MinSize() fyne.Size { return fyne.NewSize(pickerItemW, pickerItemH) }

var (
	_ desktop.Mouseable   = (*pickerItem)(nil)
	_ fyne.Tappable       = (*pickerItem)(nil)
	_ fyne.DoubleTappable = (*pickerItem)(nil)
	_ fyne.Focusable      = (*pickerItem)(nil)
	_ fyne.Shortcutable   = (*pickerItem)(nil)
)

func (it *pickerItem) MouseDown(e *desktop.MouseEvent) { it.mods = e.Modifier }
func (it *pickerItem) MouseUp(*desktop.MouseEvent)     {}
func (it *pickerItem) Tapped(*fyne.PointEvent)         { it.p.click(it.index, it.mods) }
func (it *pickerItem) DoubleTapped(*fyne.PointEvent)   { it.p.activate(it.index) }

func (it *pickerItem) FocusGained()                  {}
func (it *pickerItem) FocusLost()                    {}
func (it *pickerItem) TypedRune(rune)                {}
func (it *pickerItem) TypedKey(e *fyne.KeyEvent)     { it.p.TypedKey(e) }
func (it *pickerItem) TypedShortcut(s fyne.Shortcut) { it.p.TypedShortcut(s) }

// Selected tiles use the primary colour for the icon backing so the inverted icon stays legible in both variants.
type tileSelectionTheme struct{}

func (tileSelectionTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameSelection {
		return theme.Current().Color(theme.ColorNamePrimary, v)
	}
	return theme.Current().Color(name, v)
}

func (tileSelectionTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.Current().Font(style)
}

func (tileSelectionTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.Current().Icon(name)
}

func (tileSelectionTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.Current().Size(name)
}
