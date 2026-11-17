package objects

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

func (b *browser) createCell() fyne.CanvasObject {
	return newCell(b.tapRow, b.activateRow, b.checkRow)
}

func (b *browser) rowAt(index int) (vm.Row, bool) {
	rows := b.snapshot().Rows
	if index < 0 || index >= len(rows) {
		return vm.Row{}, false
	}
	return rows[index], true
}

func (b *browser) updateCell(id widget.TableCellID, o fyne.CanvasObject) {
	c := o.(*cell)
	row, ok := b.rowAt(id.Row)
	c.row = id.Row
	c.updating = true
	defer func() { c.updating = false }()
	c.check.Hide()
	c.icon.Hide()
	c.label.TextStyle = fyne.TextStyle{Monospace: id.Col != colName}
	c.label.SetText("")
	if !ok {
		return
	}
	switch id.Col {
	case colSelect:
		if row.Kind != vm.RowLoadMore {
			c.check.Show()
			c.check.SetChecked(row.Selected)
		}
	case colName:
		c.icon.SetResource(rowIcon(row))
		c.icon.Show()
		c.label.SetText(row.Name)
	case colSize:
		if row.Kind == vm.RowObject {
			c.label.SetText(shell.FormatBytes(row.Object.SizeBytes))
		}
	case colType:
		if row.Kind == vm.RowObject {
			c.label.SetText(row.Object.ContentType)
		}
	case colModified:
		if row.Kind == vm.RowObject && !row.Object.LastModified.IsZero() {
			c.label.SetText(row.Object.LastModified.Local().Format(dateLayout))
		}
	case colEncrypted:
		if row.Kind == vm.RowObject && row.Object.Encrypted {
			c.icon.SetResource(theme.NewSuccessThemedResource(theme.VisibilityOffIcon()))
			c.icon.Show()
			c.label.SetText(encryptedLabel)
		}
	}
	c.Refresh()
}

func rowIcon(row vm.Row) fyne.Resource {
	switch row.Kind {
	case vm.RowFolder:
		return theme.FolderIcon()
	case vm.RowLoadMore:
		return theme.MoreHorizontalIcon()
	}
	if vm.PreviewSupported(row.Object.ContentType) {
		return theme.FileImageIcon()
	}
	return theme.FileIcon()
}

func (b *browser) tapRow(index int) {
	b.table.Select(widget.TableCellID{Row: index, Col: colName})
}

func (b *browser) checkRow(index int, _ bool) {
	row, ok := b.rowAt(index)
	if !ok {
		return
	}
	switch row.Kind {
	case vm.RowFolder:
		b.vmodel.ToggleFolder(row.Key)
	case vm.RowObject:
		b.vmodel.ToggleObject(row.Key)
	}
}

func (b *browser) onRowSelected(id widget.TableCellID) {
	b.mu.Lock()
	b.focusRow = id.Row
	b.mu.Unlock()
	row, ok := b.rowAt(id.Row)
	if !ok {
		return
	}
	switch row.Kind {
	case vm.RowLoadMore:
		go b.vmodel.LoadMore(b.commandContext())
	case vm.RowObject:
		b.focusObject(row.Key)
	}
}

func (b *browser) focusObject(key string) {
	ctx, cancel := context.WithCancel(b.commandContext())
	b.mu.Lock()
	if b.cancelFocus != nil {
		b.cancelFocus()
	}
	b.cancelFocus = cancel
	b.mu.Unlock()
	go b.vmodel.Focus(ctx, key)
}

func (b *browser) focusedRow() (vm.Row, bool) {
	b.mu.Lock()
	index := b.focusRow
	b.mu.Unlock()
	return b.rowAt(index)
}

func (b *browser) activateRow(index int) {
	row, ok := b.rowAt(index)
	if !ok {
		return
	}
	switch row.Kind {
	case vm.RowFolder:
		go b.vmodel.OpenFolder(b.commandContext(), row.Key)
	case vm.RowObject:
		b.focusObject(row.Key)
		b.askOpenFile(row)
	}
}

func (b *browser) activateFocusedRow() {
	b.mu.Lock()
	index := b.focusRow
	b.mu.Unlock()
	b.activateRow(index)
}

func (b *browser) toggleFocusedRow() {
	row, ok := b.focusedRow()
	if !ok {
		return
	}
	b.checkRow(b.focusRowIndex(), !row.Selected)
}

func (b *browser) focusRowIndex() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.focusRow
}
