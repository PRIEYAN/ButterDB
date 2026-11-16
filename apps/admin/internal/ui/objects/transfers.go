package objects

import (
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	statusWaiting    = "Waiting"
	statusSaving     = "Saving to Telegram…"
	statusUploaded   = "Uploaded"
	statusDownloaded = "Downloaded"
	statusCancelled  = "Cancelled"
	statusSkipped    = "Skipped"
	statusFailed     = "Failed"
	finishingLabel   = "Finishing…"
	transferListRows = 4
)

type transferPanel struct {
	root   *fyne.Container
	owner  *browser
	title  *widget.Label
	pause  *widget.Label
	rows   *fyne.Container
	latest vm.TransferList
}

func newTransferPanel(owner *browser) *transferPanel {
	p := &transferPanel{owner: owner}
	p.title = widget.NewLabelWithStyle("Transfers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.pause = widget.NewLabel("")
	p.pause.Hide()
	clear := widget.NewButtonWithIcon("Clear finished", theme.DeleteIcon(), func() { owner.vmodel.ClearFinished() })
	p.rows = container.NewVBox()
	scroll := container.NewVScroll(p.rows)
	scroll.SetMinSize(fyne.NewSize(0, 120))
	p.root = container.NewBorder(container.NewVBox(widget.NewSeparator(), container.NewBorder(nil, nil, p.title, clear), p.pause), nil, nil, nil, scroll)
	p.root.Hide()
	return p
}

func (p *transferPanel) bind(v *vm.ViewModel) {
	v.Transfers().Subscribe(func(list vm.TransferList) { p.owner.ui(func() { p.render(list) }) })
}

func (p *transferPanel) render(list vm.TransferList) {
	p.latest = list
	if len(list.Items) == 0 {
		p.root.Hide()
		return
	}
	p.root.Show()
	p.rows.Objects = nil
	for _, item := range list.Items {
		p.rows.Add(p.rowFor(item))
	}
	p.rows.Refresh()
	p.renderPause(list)
}

func (p *transferPanel) renderPause(list vm.TransferList) {
	remaining := list.PausedUntil.Sub(p.owner.now())
	if list.PausedUntil.IsZero() || remaining <= 0 {
		p.pause.Hide()
		return
	}
	p.pause.SetText("Telegram asked the queue to wait. Resuming in " + strconv.Itoa(int((remaining+time.Second-1)/time.Second)) + " s.")
	p.pause.Show()
	go p.tickPause()
}

func (p *transferPanel) tickPause() {
	ctx := p.owner.transferContext()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	p.owner.ui(func() { p.renderPause(p.latest) })
}

func (p *transferPanel) rowFor(item vm.Transfer) fyne.CanvasObject {
	name := widget.NewLabel(item.Key)
	name.Truncation = fyne.TextTruncateEllipsis
	kind := widget.NewIcon(theme.UploadIcon())
	if item.Kind == vm.KindDownload {
		kind = widget.NewIcon(theme.DownloadIcon())
	}
	status := widget.NewLabel(TransferStatusText(item))
	status.Wrapping = fyne.TextWrapWord
	buttons := p.buttonsFor(item)
	left := container.NewBorder(nil, nil, kind, nil, name)
	return container.NewBorder(nil, nil, left, buttons, container.NewVBox(progressFor(item), status))
}

func progressFor(item vm.Transfer) fyne.CanvasObject {
	switch item.State {
	case vm.TransferCommitting:
		bar := widget.NewProgressBarInfinite()
		return bar
	case vm.TransferSending, vm.TransferDone:
		bar := widget.NewProgressBar()
		bar.TextFormatter = func() string { return "" }
		if item.Total > 0 {
			bar.Max = float64(item.Total)
			bar.Value = float64(item.Sent)
		} else if item.State == vm.TransferDone {
			bar.Max, bar.Value = 1, 1
		}
		return bar
	}
	return widget.NewLabel("")
}

func (p *transferPanel) buttonsFor(item vm.Transfer) fyne.CanvasObject {
	switch item.State {
	case vm.TransferQueued, vm.TransferSending:
		return widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() { p.owner.vmodel.Cancel(item.ID) })
	case vm.TransferCommitting:
		button := widget.NewButtonWithIcon(finishingLabel, theme.CancelIcon(), nil)
		button.Disable()
		return button
	case vm.TransferFailed, vm.TransferCancelled:
		return container.NewHBox(
			widget.NewButtonWithIcon("Retry", theme.ViewRefreshIcon(), func() { p.owner.retry(item) }),
			widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), func() { p.owner.vmodel.Remove(item.ID) }),
		)
	}
	return widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), func() { p.owner.vmodel.Remove(item.ID) })
}

func TransferStatusText(item vm.Transfer) string {
	switch item.State {
	case vm.TransferQueued:
		return statusWaiting
	case vm.TransferSending:
		return sendingText(item)
	case vm.TransferCommitting:
		if item.Message != "" {
			return item.Message
		}
		return statusSaving
	case vm.TransferDone:
		return doneText(item)
	case vm.TransferFailed:
		if item.Problem != nil {
			return item.Problem.Title
		}
		return statusFailed
	case vm.TransferCancelled:
		return statusCancelled
	case vm.TransferSkipped:
		return statusSkipped
	}
	return ""
}

func sendingText(item vm.Transfer) string {
	text := shell.FormatBytes(item.Sent)
	if item.Total > 0 {
		text += " of " + shell.FormatBytes(item.Total)
	}
	if item.BytesPerSecond > 0 {
		text += " · " + shell.FormatBytes(int64(item.BytesPerSecond)) + "/s"
	}
	return text
}

func doneText(item vm.Transfer) string {
	if item.Kind == vm.KindDownload {
		return statusDownloaded
	}
	if item.ETag != "" {
		return statusUploaded + " · " + item.ETag
	}
	return statusUploaded
}

func (b *browser) retry(item vm.Transfer) {
	ctx := b.transferContext()
	go func() {
		if item.Kind == vm.KindUpload {
			b.vmodel.RetryUpload(ctx, item.ID, b.prompts())
			return
		}
		b.vmodel.RetryDownload(ctx, item.ID, b.prompts())
	}()
}
