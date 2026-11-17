package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	brandName    = "butterDB"
	brandKicker  = "admin console"
	sidebarWidth = 15
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
	project *widget.Label
	root    fyne.CanvasObject
	entries []sidebarEntry
	syncing bool
	onPick  func(model.Route)
}

func newSidebar(onPick func(model.Route), onSwitchProject func()) *sidebar {
	sb := &sidebar{onPick: onPick}
	sb.list = widget.NewList(
		func() int { return len(sb.entries) },
		newSidebarItem,
		sb.update,
	)
	sb.list.OnSelected = sb.selected
	switchProject := Action(widget.NewButtonWithIcon("Switch project", theme.LogoutIcon(), onSwitchProject))
	sb.project = NewKicker(brandKicker)
	sb.project.Wrapping = fyne.TextWrapWord
	brand := container.NewVBox(NewSectionTitle(brandName), sb.project)
	header := container.NewVBox(Inset(gridUnit, gridUnit, gridUnit, gridUnit, brand), widget.NewSeparator(), VerticalSpace(gridUnit))
	footer := container.NewVBox(widget.NewSeparator(), Inset(gridUnit, gridUnit, gridUnit, gridUnit, switchProject))
	width := FixedSpace(theme.TextSize()*sidebarWidth, 0)
	body := container.NewBorder(header, footer, nil, nil, container.NewStack(width, sb.list))
	sb.root = container.NewBorder(nil, nil, nil, widget.NewSeparator(), body)
	return sb
}

func (sb *sidebar) setProject(name string) {
	if name == "" {
		sb.project.SetText(brandKicker)
		return
	}
	sb.project.SetText(name)
}

func newSidebarItem() fyne.CanvasObject {
	title := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	reason := NewKicker("")
	reason.Importance = widget.LowImportance
	reason.Hide()
	return container.NewHBox(widget.NewIcon(theme.FileIcon()), container.NewVBox(title, reason))
}

func sidebarItemHeight(withReason bool) float32 {
	item := newSidebarItem().(*fyne.Container)
	reason := item.Objects[1].(*fyne.Container).Objects[1].(*widget.Label)
	reason.SetText(brandKicker)
	if withReason {
		reason.Show()
	}
	return item.MinSize().Height
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
	plain, tall := sidebarItemHeight(false), sidebarItemHeight(true)
	for i, entry := range entries {
		if entry.disabled {
			sb.list.SetItemHeight(i, tall)
			continue
		}
		sb.list.SetItemHeight(i, plain)
	}
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
