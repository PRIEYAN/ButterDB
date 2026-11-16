package settings

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type activityPanel struct {
	w        *view
	root     fyne.CanvasObject
	filter   *widget.Select
	rows     *fyne.Container
	loadMore *widget.Button
	syncing  bool
}

func newActivityPanel(w *view) *activityPanel {
	p := &activityPanel{w: w, rows: container.NewVBox()}
	options := make([]string, 0, len(vm.AuditFilters()))
	for _, prefix := range vm.AuditFilters() {
		options = append(options, filterLabel(prefix))
	}
	p.filter = widget.NewSelect(options, nil)
	p.filter.SetSelected(filterLabel(""))
	p.filter.OnChanged = p.onFilter
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		w.run(func(ctx context.Context) { w.vm.RefreshActivity(ctx) })
	})
	p.loadMore = widget.NewButtonWithIcon("Load more", theme.MoreVerticalIcon(), func() {
		w.run(func(ctx context.Context) { w.vm.LoadMoreActivity(ctx) })
	})
	p.loadMore.Hide()
	toolbar := container.NewVBox(container.NewBorder(nil, nil, container.NewHBox(heading("Filter"), shell.FramedInput(p.filter)), shell.Action(refresh)), shell.VerticalSpace(gridGap))
	more := container.NewHBox(shell.Action(p.loadMore))
	p.root = sectionCard(stepActivity, "Activity", container.NewBorder(toolbar, more, nil, nil, scrolled(p.rows)))
	return p
}

func (p *activityPanel) object() fyne.CanvasObject { return p.root }

func (p *activityPanel) onFilter(label string) {
	if p.syncing {
		return
	}
	prefix := prefixForLabel(label)
	p.w.run(func(ctx context.Context) { p.w.vm.SetAuditFilter(ctx, prefix) })
}

func prefixForLabel(label string) string {
	for _, prefix := range vm.AuditFilters() {
		if filterLabel(prefix) == label {
			return prefix
		}
	}
	return ""
}

func (p *activityPanel) update(s vm.State) {
	a := s.Activity
	p.syncing = true
	p.filter.SetSelected(filterLabel(a.Filter))
	p.syncing = false
	now := p.w.vm.Now()
	rows := []fyne.CanvasObject{shell.NewWeightedRow(auditWeights(), auditHeader()...), widget.NewSeparator()}
	for _, e := range a.Events {
		rows = append(rows, shell.NewWeightedRow(auditWeights(),
			dataCell(auditTimeText(e.At, now), true),
			cell(vm.ActorText(e.Actor)),
			dataCell(e.Action, true),
			cell(orDash(e.Target)),
			cell(orDash(e.Outcome)),
			dataCell(orDash(e.ErrorCode), true),
			dataCell(orDash(e.RemoteAddr), true),
		), widget.NewSeparator())
	}
	fill(p.rows, rows)
	if a.HasMore() {
		p.loadMore.Show()
		return
	}
	p.loadMore.Hide()
}

func auditHeader() []fyne.CanvasObject {
	names := []string{"Time", "Actor", "Action", "Target", "Outcome", "Error code", "Address"}
	cells := make([]fyne.CanvasObject, len(names))
	for i, name := range names {
		cells[i] = widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	return cells
}
