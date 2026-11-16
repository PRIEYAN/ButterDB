package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type routeState struct {
	disabled bool
	reason   string
}

type sidebarEntry struct {
	route    model.Route
	title    string
	icon     fyne.Resource
	disabled bool
	reason   string
}

type sidebar struct {
	list    *widget.List
	root    fyne.CanvasObject
	entries []sidebarEntry
	syncing bool
	onPick  func(model.Route)
}

func newSidebar(onPick func(model.Route), onSwitchServer func()) *sidebar {
	sb := &sidebar{onPick: onPick}
	sb.list = widget.NewList(
		func() int { return len(sb.entries) },
		func() fyne.CanvasObject {
			title := widget.NewLabel("")
			reason := widget.NewLabel("")
			reason.Importance = widget.LowImportance
			return container.NewHBox(widget.NewIcon(theme.FileIcon()), container.NewVBox(title, reason))
		},
		sb.update,
	)
	sb.list.OnSelected = sb.selected
	switchServer := widget.NewButtonWithIcon("Switch server", theme.LogoutIcon(), onSwitchServer)
	sb.root = container.NewBorder(nil, switchServer, nil, nil, sb.list)
	return sb
}

func (sb *sidebar) update(id widget.ListItemID, item fyne.CanvasObject) {
	if id < 0 || id >= len(sb.entries) {
		return
	}
	entry := sb.entries[id]
	row := item.(*fyne.Container)
	icon := row.Objects[0].(*widget.Icon)
	texts := row.Objects[1].(*fyne.Container)
	title := texts.Objects[0].(*widget.Label)
	reason := texts.Objects[1].(*widget.Label)
	icon.SetResource(entry.icon)
	title.SetText(entry.title)
	title.Importance = widget.MediumImportance
	reason.SetText("")
	reason.Hide()
	if entry.disabled {
		title.Importance = widget.LowImportance
		reason.SetText(entry.reason)
		reason.Show()
	}
	title.Refresh()
}

func (sb *sidebar) selected(id widget.ListItemID) {
	if sb.syncing || id < 0 || id >= len(sb.entries) {
		return
	}
	entry := sb.entries[id]
	if entry.disabled {
		sb.syncing = true
		sb.list.Unselect(id)
		sb.syncing = false
		return
	}
	sb.onPick(entry.route)
}

func (sb *sidebar) setEntries(entries []sidebarEntry, current model.Route) {
	sb.entries = entries
	sb.list.Refresh()
	sb.markCurrent(current)
}

func (sb *sidebar) markCurrent(current model.Route) {
	sb.syncing = true
	defer func() { sb.syncing = false }()
	for i, entry := range sb.entries {
		if entry.route == current {
			sb.list.Select(i)
			return
		}
	}
	sb.list.UnselectAll()
}

func (sb *sidebar) show(visible bool) {
	if visible {
		sb.root.Show()
		return
	}
	sb.root.Hide()
}
