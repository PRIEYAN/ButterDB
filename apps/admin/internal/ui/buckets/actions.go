package buckets

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
)

func (s *Screen) openSelected() {
	name := s.list.selected
	if name == "" {
		return
	}
	s.browse(name)
}

func (s *Screen) browse(name string) {
	s.nav.Navigate(model.RouteObjects, model.Params{model.ParamBucket: name})
}

func (s *Screen) confirmDelete() {
	name := s.list.selected
	win := s.window()
	if name == "" || win == nil {
		return
	}
	ctx := s.ctx
	shell.ConfirmTyped(win, "Delete bucket",
		`Delete the bucket "`+name+`"? This cannot be undone.`,
		name, "Delete bucket",
		func() { s.spawn(func() { s.vm.Delete(ctx, name) }) })
}

func (s *Screen) showNotEmptyOnce(n *vm.NotEmpty) {
	if n == nil {
		s.shownNotEmpty = nil
		return
	}
	if n == s.shownNotEmpty {
		return
	}
	s.shownNotEmpty = n
	win := s.window()
	if win == nil {
		return
	}
	s.showNotEmpty(win, *n)
}

func (s *Screen) showNotEmpty(win fyne.Window, n vm.NotEmpty) {
	var dlg *dialog.CustomDialog
	closeDialog := func() {
		dlg.Hide()
		s.vm.DismissNotEmpty()
	}
	open := widget.NewButtonWithIcon("Open in object browser", theme.FolderOpenIcon(), func() {
		closeDialog()
		s.browse(n.Bucket)
	})
	closeButton := widget.NewButtonWithIcon("Close", theme.CancelIcon(), closeDialog)
	content := shell.NewProblemView(n.Problem, s.clipboard())
	dlg = dialog.NewCustomWithoutButtons("Bucket not empty", content, win)
	dlg.SetButtons([]fyne.CanvasObject{open, closeButton})
	dlg.SetOnClosed(s.vm.DismissNotEmpty)
	dlg.Show()
	win.Canvas().Focus(closeButton)
}

func (s *Screen) openCreate() {
	win := s.window()
	if win == nil {
		return
	}
	form := newBucketForm(vm.NewBucket{CacheMaxAgeSeconds: vm.DefaultCacheMaxAgeSeconds}, true, s.clipboard())
	s.runFormDialog(win, "New bucket", "Create bucket", theme.ContentAddIcon(), form, func(ctx context.Context) bool {
		return s.vm.Create(ctx, form.newBucket())
	})
}

func (s *Screen) openSettings() {
	name := s.list.selected
	win := s.window()
	if name == "" || win == nil {
		return
	}
	current := s.currentSettings(name)
	form := newBucketForm(vm.NewBucket{
		PublicRead:         current.PublicRead,
		Encrypted:          current.Encrypted,
		CacheMaxAgeSeconds: current.CacheMaxAgeSeconds,
	}, false, s.clipboard())
	s.runFormDialog(win, "Settings for "+name, "Save settings", theme.ConfirmIcon(), form, func(ctx context.Context) bool {
		return s.vm.Update(ctx, name, form.settings())
	})
}

func (s *Screen) currentSettings(name string) vm.Settings {
	for _, b := range s.vm.State().Get().Buckets {
		if b.Name == name {
			return vm.SettingsOf(b)
		}
	}
	return vm.Settings{CacheMaxAgeSeconds: vm.DefaultCacheMaxAgeSeconds}
}

func (s *Screen) runFormDialog(win fyne.Window, title, submitLabel string, icon fyne.Resource, form *bucketForm, submit func(context.Context) bool) {
	var dlg *dialog.CustomDialog
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() { dlg.Hide() })
	confirm := widget.NewButtonWithIcon(submitLabel, icon, nil)
	confirm.Importance = widget.HighImportance
	body := container.NewVBox(form.root)
	dlg = dialog.NewCustomWithoutButtons(title, body, win)
	dlg.SetButtons([]fyne.CanvasObject{cancel, confirm})
	confirm.OnTapped = func() {
		ctx := s.ctx
		form.setBusy(true)
		confirm.Disable()
		cancel.Disable()
		s.spawn(func() {
			ok := submit(ctx)
			fyne.Do(func() {
				form.setBusy(false)
				confirm.Enable()
				cancel.Enable()
				if ok {
					dlg.Hide()
					return
				}
				s.moveProblemIntoForm(form)
			})
		})
	}
	dlg.Show()
	form.focusFirst(win.Canvas())
}

func (s *Screen) moveProblemIntoForm(form *bucketForm) {
	st := s.vm.State().Get()
	if st.FormProblem != nil {
		form.showProblem(st.FormProblem)
		s.vm.ClearFormProblem()
		return
	}
	if st.Problem != nil && st.Problem.Presentation != model.PresentRoute {
		form.showProblem(st.Problem)
		s.vm.DismissProblem()
	}
}
