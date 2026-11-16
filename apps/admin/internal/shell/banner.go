package shell

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type banner struct {
	root      *fyne.Container
	clip      model.Clipboard
	now       func() time.Time
	onRetry   func()
	animation *fyne.Animation
	tick      func(progress float32)
}

func newBanner(clip model.Clipboard, now func() time.Time, onRetry func()) *banner {
	b := &banner{root: container.NewVBox(), clip: clip, now: now, onRetry: onRetry}
	b.root.Hide()
	return b
}

func (b *banner) object() fyne.CanvasObject {
	return b.root
}

func (b *banner) visible() bool {
	return b.root.Visible()
}

func (b *banner) clear() {
	b.stopCountdown()
	b.root.Objects = nil
	b.root.Hide()
	b.root.Refresh()
}

func (b *banner) showInfo(text string) {
	b.stopCountdown()
	label := newWrappedLabel(text)
	dismiss := widget.NewButtonWithIcon("Dismiss", theme.CancelIcon(), b.clear)
	row := container.NewBorder(nil, nil, nil, container.NewVBox(Action(dismiss)), label)
	b.set(NewCallout(CalloutInfo, row))
}

func (b *banner) showProblem(p model.Problem) {
	b.stopCountdown()
	dismiss := widget.NewButtonWithIcon("Dismiss", theme.CancelIcon(), b.clear)
	buttons := container.NewHBox()
	retry := b.retryButton(p)
	if retry != nil {
		buttons.Add(Action(retry))
	}
	buttons.Add(Action(dismiss))
	rows := []fyne.CanvasObject{NewFieldLabel(p.Title)}
	rows[0].(*widget.Label).Wrapping = fyne.TextWrapWord
	if p.Body != "" {
		rows = append(rows, newWrappedLabel(p.Body))
	}
	if p.Hint != "" {
		rows = append(rows, newWrappedLabel(p.Hint))
	}
	if remaining := p.RetryAt.Sub(b.now()); remaining > 0 {
		countdown := widget.NewLabel(countdownText(remaining))
		rows = append(rows, countdown)
		b.startCountdown(remaining, countdown, retry)
	}
	if text := detailsText(p); text != "" {
		rows = append(rows, newDetailsExpander(text, b.clip))
	}
	row := container.NewBorder(nil, nil, nil, container.NewVBox(buttons), container.NewVBox(rows...))
	b.set(NewCallout(CalloutWarning, row))
}

func (b *banner) retryButton(p model.Problem) *widget.Button {
	if !p.Retryable || b.onRetry == nil {
		return nil
	}
	retry := widget.NewButtonWithIcon("Retry", theme.ViewRefreshIcon(), func() {
		b.clear()
		b.onRetry()
	})
	if p.RetryAt.After(b.now()) {
		retry.Disable()
	}
	return retry
}

func (b *banner) set(content fyne.CanvasObject) {
	b.root.Objects = []fyne.CanvasObject{content, VerticalSpace(2 * gridUnit)}
	b.root.Show()
	b.root.Refresh()
}

func (b *banner) startCountdown(total time.Duration, label *widget.Label, retry *widget.Button) {
	last := ""
	b.tick = func(progress float32) {
		text := countdownText(time.Duration(float64(total) * float64(1-progress)))
		if text == last {
			return
		}
		last = text
		label.SetText(text)
		if retry == nil {
			return
		}
		if progress >= 1 {
			retry.Enable()
			return
		}
		retry.Disable()
	}
	b.animation = fyne.NewAnimation(total, b.tick)
	b.animation.Curve = fyne.AnimationLinear
	b.animation.Start()
}

func (b *banner) stopCountdown() {
	if b.animation != nil {
		b.animation.Stop()
		b.animation = nil
		b.tick = nil
	}
}
