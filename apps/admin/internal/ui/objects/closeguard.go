package objects

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func (b *browser) askQuitWithTransfers(active int) {
	text := strconv.Itoa(active) + " transfers are running. Uploads already saving to Telegram will finish on the server."
	if active == 1 {
		text = "1 transfer is running. Uploads already saving to Telegram will finish on the server."
	}
	body := widget.NewLabel(text)
	body.Wrapping = fyne.TextWrapWord
	dlg := newChoiceDialog(b.win, "objects/quit", closeGuardTitle, body)
	keep := widget.NewButton(keepOpenLabel, func() { dlg.Hide() })
	quit := widget.NewButton(quitAnywayLabel, func() {
		dlg.Hide()
		b.win.Close()
	})
	quit.Importance = widget.DangerImportance
	dlg.SetButtons(keep, quit)
	dlg.Show()
	b.win.Canvas().Focus(keep)
}
