package connect

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	storageTitle    = "Where should this device keep its sign-in?"
	storageBody     = "The operating system keychain is not available on this computer."
	sessionChoice   = "Keep me signed in for this session only"
	fileChoice      = "Store the token in a protected file"
	sessionWarning  = "The device stays registered on the server, and the next start needs a new pairing code. Revoke stale devices in Settings."
	fileWarning     = "Any program running as this user can read the file."
	continueLabel   = "Continue"
	storageCancelOf = "Cancel"
)

type storagePanel struct {
	w       *view
	box     *fyne.Container
	choice  *widget.RadioGroup
	warning *widget.Label
	proceed *actionButton
	cancel  *actionButton
}

func newStoragePanel(w *view) *storagePanel {
	p := &storagePanel{w: w}
	p.warning = newWrappedLabel(sessionWarning)
	p.choice = widget.NewRadioGroup([]string{sessionChoice, fileChoice}, p.onChoice)
	p.choice.SetSelected(sessionChoice)
	p.proceed = newActionButton(continueLabel, theme.ConfirmIcon(), p.onProceed)
	p.proceed.Importance = widget.HighImportance
	p.cancel = newActionButton(storageCancelOf, theme.CancelIcon(), w.vm.Cancel)
	p.cancel.onEscape = w.vm.Cancel
	p.proceed.onEscape = w.vm.Cancel
	p.box = container.NewVBox(
		newHeading(storageTitle),
		newWrappedLabel(storageBody),
		p.choice,
		p.warning,
		container.NewHBox(p.cancel, p.proceed),
	)
	return p
}

func (p *storagePanel) object() fyne.CanvasObject { return p.box }

func (p *storagePanel) focusFirst() { focusOn(p.cancel) }

func (p *storagePanel) focusField(string) bool { return false }

func (p *storagePanel) onChoice(selected string) {
	if selected == fileChoice {
		p.warning.SetText(fileWarning)
		return
	}
	p.warning.SetText(sessionWarning)
}

func (p *storagePanel) selectedKind() model.TokenStorage {
	if p.choice.Selected == fileChoice {
		return model.TokenFile
	}
	return model.TokenMemory
}

func (p *storagePanel) onProceed() {
	kind := p.selectedKind()
	p.w.run(func(ctx context.Context) { p.w.vm.ChooseStorage(ctx, kind) })
}

func (p *storagePanel) update(s vm.State) {
	if s.Loading {
		p.proceed.Disable()
		return
	}
	p.proceed.Enable()
}
