package buckets

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type toolbarActions struct {
	create   func()
	settings func()
	browse   func()
	remove   func()
	refresh  func()
}

type toolbar struct {
	object   *fyne.Container
	settings *widget.Button
	browse   *widget.Button
	remove   *widget.Button
}

func newToolbar(a toolbarActions) toolbar {
	t := toolbar{
		settings: widget.NewButtonWithIcon("Settings…", theme.SettingsIcon(), a.settings),
		browse:   widget.NewButtonWithIcon("Browse objects", theme.FolderOpenIcon(), a.browse),
		remove:   widget.NewButtonWithIcon("Delete…", theme.DeleteIcon(), a.remove),
	}
	create := widget.NewButtonWithIcon("New bucket", theme.ContentAddIcon(), a.create)
	create.Importance = widget.HighImportance
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), a.refresh)
	t.object = container.NewHBox(create, t.settings, t.browse, t.remove, refresh)
	t.setSelection(false)
	return t
}

func (t toolbar) setSelection(selected bool) {
	for _, b := range []*widget.Button{t.settings, t.browse, t.remove} {
		if selected {
			b.Enable()
		} else {
			b.Disable()
		}
	}
}
