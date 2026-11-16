package buckets

import (
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
)

type column struct {
	title string
	sort  vm.SortColumn
	ems   float32
}

func columns() []column {
	return []column{
		{title: "Name", sort: vm.ColumnName, ems: 16},
		{title: "Access", sort: vm.ColumnAccess, ems: 8},
		{title: "Encryption", sort: vm.ColumnEncryption, ems: 8},
		{title: "Cache max-age", sort: vm.ColumnCacheMaxAge, ems: 9},
		{title: "Objects", sort: vm.ColumnObjects, ems: 7},
		{title: "Size", sort: vm.ColumnSize, ems: 8},
		{title: "Created", sort: vm.ColumnCreated, ems: 12},
	}
}

type bucketTable struct {
	widget.Table
	onActivate func()
	onMove     func(delta int)
}

func (t *bucketTable) TypedKey(ev *fyne.KeyEvent) {
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

func (t *bucketTable) DoubleTapped(*fyne.PointEvent) {
	t.onActivate()
}

type bucketList struct {
	table      *bucketTable
	cols       []column
	buckets    []adminv1.Bucket
	sortColumn vm.SortColumn
	descending bool
	now        func() time.Time
	selected   string
	onSort     func(vm.SortColumn)
	onSelected func()
}

func newBucketList(now func() time.Time, onSort func(vm.SortColumn), onSelected func(), onActivate func()) *bucketList {
	l := &bucketList{cols: columns(), now: now, onSort: onSort, onSelected: onSelected}
	t := &bucketTable{onActivate: onActivate, onMove: l.moveSelection}
	t.Length = func() (int, int) { return len(l.buckets), len(l.cols) }
	t.CreateCell = func() fyne.CanvasObject {
		label := widget.NewLabel("")
		label.Truncation = fyne.TextTruncateEllipsis
		return label
	}
	t.UpdateCell = l.updateCell
	t.ShowHeaderRow = true
	t.CreateHeader = func() fyne.CanvasObject {
		return widget.NewButtonWithIcon("", theme.MenuDropDownIcon(), nil)
	}
	t.UpdateHeader = l.updateHeader
	t.OnSelected = func(id widget.TableCellID) { l.selectRow(id.Row) }
	t.ExtendBaseWidget(t)
	for i, c := range l.cols {
		t.SetColumnWidth(i, theme.TextSize()*c.ems)
	}
	l.table = t
	return l
}

func (l *bucketList) setBuckets(buckets []adminv1.Bucket, sortColumn vm.SortColumn, descending bool) {
	l.buckets, l.sortColumn, l.descending = buckets, sortColumn, descending
	l.table.Refresh()
	l.restoreSelection()
}

func (l *bucketList) selectRow(row int) {
	if row < 0 || row >= len(l.buckets) {
		return
	}
	l.selected = l.buckets[row].Name
	l.onSelected()
}

func (l *bucketList) restoreSelection() {
	row := l.rowOf(l.selected)
	if row < 0 {
		l.selected = ""
		l.table.UnselectAll()
		return
	}
	l.table.Select(widget.TableCellID{Row: row, Col: 0})
}

func (l *bucketList) moveSelection(delta int) {
	row := l.rowOf(l.selected) + delta
	if l.selected == "" {
		row = 0
	}
	if row < 0 || row >= len(l.buckets) {
		return
	}
	l.table.Select(widget.TableCellID{Row: row, Col: 0})
}

func (l *bucketList) rowOf(name string) int {
	for i, b := range l.buckets {
		if b.Name == name {
			return i
		}
	}
	return -1
}

func (l *bucketList) updateHeader(id widget.TableCellID, o fyne.CanvasObject) {
	b := o.(*widget.Button)
	if id.Col < 0 || id.Col >= len(l.cols) {
		b.SetText("")
		b.OnTapped = nil
		return
	}
	c := l.cols[id.Col]
	b.SetText(c.title + l.sortMark(c.sort))
	b.Importance = widget.LowImportance
	b.Icon = nil
	b.OnTapped = func() { l.onSort(c.sort) }
	b.Refresh()
}

func (l *bucketList) sortMark(column vm.SortColumn) string {
	if column != l.sortColumn {
		return ""
	}
	if l.descending {
		return " (descending)"
	}
	return " (ascending)"
}

func (l *bucketList) updateCell(id widget.TableCellID, o fyne.CanvasObject) {
	label := o.(*widget.Label)
	if id.Row < 0 || id.Row >= len(l.buckets) {
		label.SetText("")
		return
	}
	label.SetText(l.cellText(l.buckets[id.Row], id.Col))
}

func (l *bucketList) cellText(b adminv1.Bucket, col int) string {
	switch l.cols[col].sort {
	case vm.ColumnName:
		return b.Name
	case vm.ColumnAccess:
		return b.Policy
	case vm.ColumnEncryption:
		return encryptionText(b.EncryptionDefault)
	case vm.ColumnCacheMaxAge:
		return vm.FormatCacheMaxAge(b.CacheMaxAgeSeconds)
	case vm.ColumnObjects:
		return strconv.FormatInt(b.ObjectCount, 10)
	case vm.ColumnSize:
		return shell.FormatBytes(b.StoredBytes)
	case vm.ColumnCreated:
		return vm.FormatCreated(b.CreatedAt, l.now())
	}
	return ""
}

func encryptionText(encryption string) string {
	if encryption == vm.EncryptionTDE1 {
		return "On"
	}
	return "Off"
}
