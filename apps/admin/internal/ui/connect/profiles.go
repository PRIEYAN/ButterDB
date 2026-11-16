package connect

import (
	"context"
	"reflect"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	trustPinnedText = "Pinned key"
	trustSystemText = "System certificate"
	forgetBody      = "This removes the server and its stored sign-in from this computer. It does not revoke this device on the server; to do that, sign in from another device and open Settings."
)

type profilesPanel struct {
	w         *view
	box       *fyne.Container
	list      *fyne.Container
	empty     *widget.Label
	urlEntry  *widget.Entry
	label     *widget.Entry
	continues *actionButton
	rows      []vm.ProfileRow
	loading   bool
}

func newProfilesPanel(w *view) *profilesPanel {
	p := &profilesPanel{w: w}
	p.list = container.NewVBox()
	p.empty = newWrappedLabel("No servers yet. Add one below.")
	p.urlEntry = widget.NewEntry()
	p.urlEntry.SetPlaceHolder("host or host:port")
	p.label = widget.NewEntry()
	p.label.SetPlaceHolder("Name (optional)")
	p.continues = newActionButton("Continue", theme.NavigateNextIcon(), p.submit)
	p.continues.Importance = widget.HighImportance
	p.urlEntry.OnSubmitted = func(string) { p.submit() }
	p.label.OnSubmitted = func(string) { p.submit() }
	form := widget.NewForm(
		widget.NewFormItem("Server address", p.urlEntry),
		widget.NewFormItem("Name", p.label),
	)
	p.box = container.NewVBox(
		shell.NewWindowCard("connect/servers", "Your servers", container.NewVBox(p.empty, p.list)),
		shell.NewSectionGap(),
		shell.NewWindowCard("connect/new-server", "Add a server", container.NewVBox(
			form,
			shell.NewSectionGap(),
			container.NewHBox(shell.Action(p.continues)),
		)),
	)
	return p
}

func (p *profilesPanel) object() fyne.CanvasObject { return p.box }

func (p *profilesPanel) focusFirst() { focusOn(p.urlEntry) }

func (p *profilesPanel) focusField(field string) bool {
	if field != vm.FieldURL {
		return false
	}
	focusOn(p.urlEntry)
	return true
}

func (p *profilesPanel) submit() {
	url, label := p.urlEntry.Text, p.label.Text
	p.w.run(func(ctx context.Context) { p.w.vm.Continue(ctx, url, label) })
}

func (p *profilesPanel) update(s vm.State) {
	if s.Loading == p.loading && reflect.DeepEqual(s.Profiles, p.rows) {
		return
	}
	p.loading = s.Loading
	p.rows = s.Profiles
	p.list.RemoveAll()
	for _, row := range s.Profiles {
		p.list.Add(p.rowFor(row, s.Loading))
	}
	p.list.Refresh()
	if len(s.Profiles) == 0 {
		p.empty.Show()
	} else {
		p.empty.Hide()
	}
	if s.Loading {
		p.continues.Disable()
	} else {
		p.continues.Enable()
	}
}

func (p *profilesPanel) rowFor(row vm.ProfileRow, loading bool) fyne.CanvasObject {
	connect := newActionButton("Connect", theme.LoginIcon(), func() {
		p.w.run(func(ctx context.Context) { p.w.vm.Connect(ctx, row.ServerID) })
	})
	connect.Importance = widget.HighImportance
	if loading {
		connect.Disable()
	}
	rename := newActionButton("Rename", theme.DocumentCreateIcon(), func() { p.rename(row) })
	forget := newActionButton("Forget", theme.DeleteIcon(), func() { p.forget(row) })
	details := shell.NewMono(row.URL + "   last used " + shell.FormatAge(row.LastUsed, p.w.now()))
	details.Truncation = fyne.TextTruncateEllipsis
	trust := shell.NewKicker(trustText(row.TrustMode))
	summary := container.NewVBox(
		newHeading(row.Label),
		details,
		container.NewHBox(widget.NewIcon(trustIcon(row.TrustMode)), trust),
	)
	actions := container.NewVBox(shell.Actions(connect, rename, forget))
	return container.NewVBox(
		container.NewBorder(nil, nil, nil, actions, summary),
		widget.NewSeparator(),
	)
}

func trustText(mode adminv1.TrustMode) string {
	if mode == adminv1.TrustPinned {
		return trustPinnedText
	}
	return trustSystemText
}

func trustIcon(mode adminv1.TrustMode) fyne.Resource {
	if mode == adminv1.TrustPinned {
		return theme.ConfirmIcon()
	}
	return theme.InfoIcon()
}

func (p *profilesPanel) rename(row vm.ProfileRow) {
	win := windowFor(p.box)
	if win == nil {
		return
	}
	entry := widget.NewEntry()
	entry.SetText(row.Label)
	dialog.ShowForm("Rename server", "Save", "Cancel", []*widget.FormItem{widget.NewFormItem("Name", entry)}, func(ok bool) {
		if ok {
			p.w.vm.Rename(row.ServerID, entry.Text)
		}
	}, win)
}

func (p *profilesPanel) forget(row vm.ProfileRow) {
	win := windowFor(p.box)
	if win == nil {
		return
	}
	shell.ConfirmDestructive(win, "Forget "+row.Label+"?", forgetBody, "Forget", func() {
		p.w.vm.Forget(row.ServerID)
	})
}
