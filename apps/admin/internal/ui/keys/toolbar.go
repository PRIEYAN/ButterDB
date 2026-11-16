package keys

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

type toolbarActions struct {
	create      func()
	edit        func()
	revoke      func()
	copyID      func()
	refresh     func()
	showRevoked func(bool)
}

type toolbar struct {
	object  *fyne.Container
	edit    *widget.Button
	revoke  *widget.Button
	copyID  *widget.Button
	revoked *widget.Check
}

func newToolbar(a toolbarActions) toolbar {
	t := toolbar{
		edit:   widget.NewButtonWithIcon("Edit…", theme.DocumentCreateIcon(), a.edit),
		revoke: widget.NewButtonWithIcon("Revoke…", theme.DeleteIcon(), a.revoke),
		copyID: widget.NewButtonWithIcon("Copy key ID", theme.ContentCopyIcon(), a.copyID),
	}
	t.revoked = widget.NewCheck("Show revoked", a.showRevoked)
	create := widget.NewButtonWithIcon("New key", theme.ContentAddIcon(), a.create)
	create.Importance = widget.HighImportance
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), a.refresh)
	t.object = container.NewHBox(shell.Actions(create, t.edit, t.revoke, t.copyID, refresh), t.revoked)
	t.setSelection(false, false)
	return t
}

func (t toolbar) setSelection(selected, active bool) {
	set(t.copyID, selected)
	set(t.edit, selected && active)
	set(t.revoke, selected && active)
}

func set(b *widget.Button, enabled bool) {
	if enabled {
		b.Enable()
		return
	}
	b.Disable()
}
