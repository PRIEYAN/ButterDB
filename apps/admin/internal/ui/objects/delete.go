package objects

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	nothingToDeleteHint = "Select at least one object to delete."
	foldersOnlyHint     = "Folders disappear when their objects are deleted. Select the objects to delete."
)

func (b *browser) deleteKeys() []string {
	st := b.snapshot()
	if len(st.SelectedObjects) > 0 {
		return st.SelectedObjects
	}
	if row, ok := b.focusedRow(); ok && row.Kind == vm.RowObject && len(st.SelectedFolders) == 0 {
		return []string{row.Key}
	}
	return nil
}

func (b *browser) requestDelete() {
	st := b.snapshot()
	keys := b.deleteKeys()
	if len(keys) == 0 {
		if len(st.SelectedFolders) > 0 {
			b.inform(foldersOnlyHint)
			return
		}
		b.inform(nothingToDeleteHint)
		return
	}
	title, body := vm.DeleteConfirmation(keys, len(st.SelectedFolders))
	shell.ConfirmDestructive(b.win, title, body, "Delete", func() { go b.runDelete(st.Bucket, keys) })
}

func (b *browser) runDelete(bucket string, keys []string) {
	result := b.vmodel.Delete(b.transferContext(), bucket, keys)
	if len(result.Failed) == 0 {
		return
	}
	b.ui(func() { b.showDeleteFailures(bucket, result) })
}

func (b *browser) showDeleteFailures(bucket string, result vm.DeleteResult) {
	rows := container.NewVBox(widget.NewLabel(failureSummary(result)))
	for _, failure := range result.Failed {
		line := widget.NewLabel(failure.Key + " — " + failure.Problem.Title)
		line.Wrapping = fyne.TextWrapBreak
		rows.Add(line)
	}
	scroll := container.NewVScroll(rows)
	scroll.SetMinSize(fyne.NewSize(480, 200))
	var dlg *dialog.CustomDialog
	closeButton := widget.NewButton(dismissLabel, func() { dlg.Hide() })
	retry := widget.NewButton("Retry failed", func() {
		dlg.Hide()
		go b.runDelete(bucket, result.FailedKeys())
	})
	dlg = newChoiceDialog(b.win, "Some objects were not deleted", scroll, closeButton, retry)
	dlg.Show()
	b.win.Canvas().Focus(closeButton)
}

func failureSummary(result vm.DeleteResult) string {
	return strconv.Itoa(len(result.Deleted)) + " deleted, " + strconv.Itoa(len(result.Failed)) + " failed."
}
