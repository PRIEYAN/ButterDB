package objects

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

func (b *browser) askOpenFile(row vm.Row) {
	bucket := b.snapshot().Bucket
	ctx := b.commandContext()
	name := shell.NewSectionTitle(row.Name)
	name.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(name, widget.NewLabel(shell.FormatBytes(row.Object.SizeBytes)), shell.NewMono(row.Object.ContentType))
	dlg := newChoiceDialog(b.win, "objects/open", "Open file", content)
	open := widget.NewButton("Open", func() {
		dlg.Hide()
		if b.opts.Opener == nil {
			newInfoDialog("objects/open", "Cannot open file", widget.NewLabel("The file viewer is unavailable."), b.win).Show()
			return
		}
		if ctx.Err() == nil {
			b.opts.Opener.Open(ctx, bucket, row.Object)
		}
	})
	open.Importance = widget.HighImportance
	dlg.SetButtons(widget.NewButton("Cancel", dlg.Hide), open)
	dlg.Show()
}
