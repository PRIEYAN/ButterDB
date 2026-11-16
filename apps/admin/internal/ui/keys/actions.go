package keys

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

const (
	statusRevoked = "revoked"
	revokeWarning = "Applications using this key stop working immediately, and presigned URLs signed with it stop working."
)

func (s *Screen) copySelectedID() {
	if key, ok := s.list.selectedKey(); ok {
		s.baseClipboard().SetText(key.AccessKeyID)
	}
}

func (s *Screen) confirmRevoke() {
	key, ok := s.list.selectedKey()
	win := s.window()
	if !ok || win == nil || key.Status == statusRevoked {
		return
	}
	ctx := s.ctx
	shell.ConfirmDestructive(win, "Revoke access key", revokeWarning, "Revoke key",
		func() { s.spawn(func() { s.vm.Revoke(ctx, key.AccessKeyID) }) })
}

func (s *Screen) showSecretOnce(secret *vm.Secret) {
	if secret == nil {
		s.secret, s.secretView = nil, nil
		return
	}
	if secret == s.secret {
		return
	}
	win := s.window()
	if win == nil || s.form != nil {
		return
	}
	s.secret = secret
	s.secretView = newSecretDialog(*secret, s.clip, win, s.vm.Done)
	s.secretView.show(win.Canvas())
}

func (s *Screen) openCreate() {
	win := s.window()
	if win == nil {
		return
	}
	ctx := s.ctx
	form := newKeyForm("", vm.KeyScope{Actions: vm.PresetReadOnly()}, s.vm.State().Get().BucketNames, s.clip)
	s.runFormDialog(win, "keys/new", "New access key", "Create key", theme.ContentAddIcon(), form, func(ctx context.Context) bool {
		return s.vm.Create(ctx, form.newKey())
	})
	s.spawn(func() {
		s.vm.LoadBuckets(ctx)
		fyne.Do(func() { s.moveFormProblem(form) })
	})
}

func (s *Screen) openEdit() {
	key, ok := s.list.selectedKey()
	win := s.window()
	if !ok || win == nil || key.Status == statusRevoked {
		return
	}
	ctx := s.ctx
	form := newKeyForm(key.Name, vm.ScopeOf(key.Scope), s.vm.State().Get().BucketNames, s.clip)
	s.runFormDialog(win, "keys/"+key.AccessKeyID+"/edit", "Edit "+key.Name, "Save changes", theme.ConfirmIcon(), form, func(ctx context.Context) bool {
		return s.vm.Update(ctx, key.AccessKeyID, form.keyEdit())
	})
	s.spawn(func() {
		s.vm.LoadBuckets(ctx)
		fyne.Do(func() { s.moveFormProblem(form) })
	})
}

func (s *Screen) runFormDialog(win fyne.Window, label, title, submitLabel string, icon fyne.Resource, form *keyForm, submit func(context.Context) bool) {
	var dlg *shell.Dialog
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() { dlg.Hide() })
	confirm := widget.NewButtonWithIcon(submitLabel, icon, nil)
	confirm.Importance = widget.HighImportance
	dlg = shell.NewDialog(win, label, title, container.NewVBox(form.root))
	dlg.SetButtons(cancel, confirm)
	dlg.SetOnClosed(func() {
		if s.form == form {
			s.form = nil
		}
		s.showSecretOnce(s.vm.State().Get().Secret)
	})
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
	s.form = form
	dlg.Show()
	win.Canvas().Focus(form.name)
}

func (s *Screen) moveFormProblem(form *keyForm) bool {
	st := s.vm.State().Get()
	if st.FormProblem == nil {
		return false
	}
	form.showProblem(st.FormProblem)
	s.vm.ClearFormProblem()
	return true
}

func (s *Screen) moveProblemIntoForm(form *keyForm) {
	if s.moveFormProblem(form) {
		return
	}
	st := s.vm.State().Get()
	if st.Problem != nil && st.Problem.Presentation != model.PresentRoute {
		form.showProblem(st.Problem)
		s.vm.DismissProblem()
	}
}
