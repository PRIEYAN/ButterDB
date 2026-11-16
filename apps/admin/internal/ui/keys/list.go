package keys

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

const (
	colName = iota
	colID
	colBuckets
	colPermissions
	colStatus
	colLastUsed
	colCreated
)

type column struct {
	title string
	ems   float32
}

func columns() []column {
	return []column{
		{title: "Name", ems: 14},
		{title: "Access key ID", ems: 18},
		{title: "Buckets", ems: 14},
		{title: "Permissions", ems: 16},
		{title: "Status", ems: 7},
		{title: "Last used", ems: 10},
		{title: "Created", ems: 12},
	}
}

type keyTable struct {
	widget.Table
	onActivate func()
	onMove     func(delta int)
	onCopy     func()
}

func (t *keyTable) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyReturn, fyne.KeyEnter:
		t.onActivate()
	case fyne.KeyDown:
		t.onMove(1)
	case fyne.KeyUp:
		t.onMove(-1)
	default:
		t.Table.TypedKey(ev)
	}
}

func (t *keyTable) TypedShortcut(s fyne.Shortcut) {
	if _, ok := s.(*fyne.ShortcutCopy); ok {
		t.onCopy()
	}
}

type keyList struct {
	table      *keyTable
	cols       []column
	keys       []adminv1.AccessKey
	selected   string
	now        func() time.Time
	onSelected func()
}

func newKeyList(now func() time.Time, onSelected, onActivate, onCopy func()) *keyList {
	l := &keyList{cols: columns(), now: now, onSelected: onSelected}
	t := &keyTable{onActivate: onActivate, onMove: l.moveSelection, onCopy: onCopy}
	t.Length = func() (int, int) { return len(l.keys), len(l.cols) }
	t.CreateCell = func() fyne.CanvasObject {
		label := widget.NewLabel("")
		label.Truncation = fyne.TextTruncateEllipsis
		return label
	}
	t.UpdateCell = l.updateCell
	t.ShowHeaderRow = true
	t.CreateHeader = func() fyne.CanvasObject { return widget.NewLabel("") }
	t.UpdateHeader = l.updateHeader
	t.OnSelected = func(id widget.TableCellID) { l.selectRow(id.Row) }
	t.ExtendBaseWidget(t)
	for i, c := range l.cols {
		t.SetColumnWidth(i, theme.TextSize()*c.ems)
	}
	l.table = t
	return l
}

func (l *keyList) setKeys(keys []adminv1.AccessKey) {
	l.keys = keys
	l.table.Refresh()
	l.restoreSelection()
}

func (l *keyList) selectedKey() (adminv1.AccessKey, bool) {
	row := l.rowOf(l.selected)
	if row < 0 {
		return adminv1.AccessKey{}, false
	}
	return l.keys[row], true
}

func (l *keyList) selectRow(row int) {
	if row < 0 || row >= len(l.keys) {
		return
	}
	l.selected = l.keys[row].AccessKeyID
	l.onSelected()
}

func (l *keyList) rowOf(id string) int {
	for i, k := range l.keys {
		if k.AccessKeyID == id {
			return i
		}
	}
	return -1
}

func (l *keyList) restoreSelection() {
	row := l.rowOf(l.selected)
	if row < 0 {
		l.selected = ""
		l.table.UnselectAll()
		return
	}
	l.table.Select(widget.TableCellID{Row: row, Col: 0})
}

func (l *keyList) moveSelection(delta int) {
	row := l.rowOf(l.selected) + delta
	if l.selected == "" {
		row = 0
	}
	if row < 0 || row >= len(l.keys) {
		return
	}
	l.table.Select(widget.TableCellID{Row: row, Col: 0})
}

func (l *keyList) updateHeader(id widget.TableCellID, o fyne.CanvasObject) {
	label := o.(*widget.Label)
	label.TextStyle = fyne.TextStyle{Bold: true}
	if id.Col < 0 || id.Col >= len(l.cols) {
		label.SetText("")
		return
	}
	label.SetText(l.cols[id.Col].title)
}

func (l *keyList) updateCell(id widget.TableCellID, o fyne.CanvasObject) {
	label := o.(*widget.Label)
	label.TextStyle = fyne.TextStyle{Monospace: id.Col != colName && id.Col != colBuckets && id.Col != colPermissions}
	if id.Row < 0 || id.Row >= len(l.keys) {
		label.SetText("")
		return
	}
	label.SetText(l.cellText(l.keys[id.Row], id.Col))
}

func (l *keyList) cellText(k adminv1.AccessKey, col int) string {
	switch col {
	case colName:
		return k.Name
	case colID:
		return k.AccessKeyID
	case colBuckets:
		return vm.BucketsText(k.Scope)
	case colPermissions:
		return vm.ActionsText(k.Scope)
	case colStatus:
		return k.Status
	case colLastUsed:
		return l.lastUsedText(k)
	case colCreated:
		return vm.FormatCreated(k.CreatedAt, l.now())
	}
	return ""
}

func (l *keyList) lastUsedText(k adminv1.AccessKey) string {
	if k.LastUsedAt == nil {
		return "never"
	}
	return shell.FormatAge(*k.LastUsedAt, l.now())
}
