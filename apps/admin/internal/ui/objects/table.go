package objects

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	colSelect = iota
	colName
	colSize
	colType
	colModified
	colEncrypted
	columnCount
)

var columnTitles = [columnCount]string{"", "Name", "Size", "Content type", "Last modified", "Encryption"}

var columnWidths = [columnCount]float32{44, 360, 100, 170, 170, 120}

type tableHandlers struct {
	onUp        func()
	onDelete    func()
	onActivate  func()
	onToggle    func()
	onSelectAll func()
}

type browserTable struct {
	widget.Table
	handlers tableHandlers
}

func newBrowserTable(handlers tableHandlers) *browserTable {
	t := &browserTable{handlers: handlers}
	t.ShowHeaderRow = true
	t.ExtendBaseWidget(t)
	return t
}

func (t *browserTable) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyBackspace:
		t.handlers.onUp()
	case fyne.KeyDelete:
		t.handlers.onDelete()
	case fyne.KeyReturn, fyne.KeyEnter:
		t.Table.TypedKey(&fyne.KeyEvent{Name: fyne.KeySpace})
		t.handlers.onActivate()
	case fyne.KeySpace:
		t.Table.TypedKey(ev)
		t.handlers.onToggle()
	default:
		t.Table.TypedKey(ev)
	}
}

func (t *browserTable) TypedShortcut(s fyne.Shortcut) {
	if _, ok := s.(*fyne.ShortcutSelectAll); ok {
		t.handlers.onSelectAll()
	}
}

type cell struct {
	widget.BaseWidget
	check     *widget.Check
	icon      *widget.Icon
	label     *widget.Label
	row       int
	updating  bool
	onTapped  func(row int)
	onDouble  func(row int)
	onToggled func(row int, checked bool)
}

func newCell(onTapped, onDouble func(row int), onToggled func(row int, checked bool)) *cell {
	c := &cell{
		icon:      widget.NewIcon(theme.FileIcon()),
		label:     widget.NewLabel(""),
		onTapped:  onTapped,
		onDouble:  onDouble,
		onToggled: onToggled,
	}
	c.label.Truncation = fyne.TextTruncateEllipsis
	c.check = widget.NewCheck("", func(checked bool) {
		if !c.updating && c.onToggled != nil {
			c.onToggled(c.row, checked)
		}
	})
	c.ExtendBaseWidget(c)
	return c
}

func (c *cell) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewBorder(nil, nil, container.NewHBox(c.check, c.icon), nil, c.label))
}

func (c *cell) Tapped(*fyne.PointEvent) {
	if c.onTapped != nil {
		c.onTapped(c.row)
	}
}

func (c *cell) DoubleTapped(*fyne.PointEvent) {
	if c.onDouble != nil {
		c.onDouble(c.row)
	}
}
