package buckets

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

type problemStrip struct {
	object  *fyne.Container
	clip    model.Clipboard
	retry   *widget.Button
	dismiss *widget.Button
	body    *fyne.Container
}

func newProblemStrip(clip model.Clipboard, onRetry, onDismiss func()) *problemStrip {
	p := &problemStrip{clip: clip}
	p.retry = widget.NewButtonWithIcon("Retry", theme.ViewRefreshIcon(), onRetry)
	p.dismiss = widget.NewButtonWithIcon("Dismiss", theme.CancelIcon(), onDismiss)
	p.body = container.NewStack()
	p.object = container.NewVBox(p.body, container.NewHBox(p.retry, p.dismiss))
	p.object.Hide()
	return p
}

func (p *problemStrip) show(problem *model.Problem) {
	if problem == nil || problem.Presentation == model.PresentRoute {
		p.object.Hide()
		return
	}
	p.body.Objects = []fyne.CanvasObject{shell.NewProblemView(*problem, p.clip)}
	p.body.Refresh()
	p.retry.Hidden = !problem.Retryable
	p.retry.Refresh()
	p.object.Show()
}

func inlineText(p *model.Problem) string {
	if p == nil {
		return ""
	}
	if p.Hint == "" {
		return p.Title
	}
	return p.Title + " " + p.Hint
}
