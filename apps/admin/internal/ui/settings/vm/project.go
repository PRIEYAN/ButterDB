package vm

import (
	"context"
	"strings"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

const (
	NoticeProjectRenamed  = "Project renamed."
	NoticePasswordChanged = "Project password changed."
	NoticeKeyReset        = "Project key reset."
	ResetConfirmation     = embedded.ResetConfirmation
)

type ResetReport = embedded.ResetReport

type Project = embedded.Project

type ProjectID = embedded.ProjectID

type ProjectState struct {
	Open    bool
	Project embedded.Project
	Report  *ResetReport
}

type AccountOutcome int

const (
	AccountFailed AccountOutcome = iota
	AccountDone
)

func (o AccountOutcome) Route() model.Route {
	if o == AccountDone {
		return model.RouteTelegram
	}
	return ""
}

func (v *ViewModel) currentProject() ProjectState {
	a := v.snapshot().Account
	if a == nil {
		return ProjectState{}
	}
	p, open := a.Current()
	return ProjectState{Open: open, Project: p}
}

func (v *ViewModel) project() (embedded.Project, Account, bool) {
	a := v.snapshot().Account
	if a == nil {
		return embedded.Project{}, nil, false
	}
	p, open := a.Current()
	return p, a, open
}

func (v *ViewModel) RenameProject(ctx context.Context, name string) bool {
	v.begin()
	name = strings.TrimSpace(name)
	if problem := model.CheckProjectName(name); problem != nil {
		v.fail(*problem)
		return false
	}
	p, a, open := v.project()
	if !open {
		v.fail(dialogProblem(textNoProject))
		return false
	}
	if err := a.Rename(ctx, p.ID, name); err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return false
	}
	v.finish(func(s *State) {
		s.Project = v.currentProject()
		s.Project.Project.Name = name
		s.Notice = NoticeProjectRenamed
	}, nil)
	return true
}

func (v *ViewModel) ChangePassword(ctx context.Context, oldPassword, newPassword, confirm []byte) bool {
	defer wipe(oldPassword, newPassword, confirm)
	v.begin()
	if len(oldPassword) == 0 {
		v.fail(model.InlineProblem(model.FieldOldPassword, textEnterOldPassword))
		return false
	}
	if problem := model.CheckNewPassword(newPassword, confirm); problem != nil {
		v.fail(*problem)
		return false
	}
	p, a, open := v.project()
	if !open {
		v.fail(dialogProblem(textNoProject))
		return false
	}
	if err := a.ChangePassword(ctx, p.ID, oldPassword, newPassword); err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return false
	}
	v.finish(func(s *State) { s.Notice = NoticePasswordChanged }, nil)
	return true
}

func (v *ViewModel) ResetKey(ctx context.Context, newPassword, confirm []byte, typed string) (ResetReport, bool) {
	defer wipe(newPassword, confirm)
	v.begin()
	if problem := model.CheckNewPassword(newPassword, confirm); problem != nil {
		v.fail(*problem)
		return ResetReport{}, false
	}
	if problem := model.CheckResetConfirmation(typed); problem != nil {
		v.fail(*problem)
		return ResetReport{}, false
	}
	p, a, open := v.project()
	if !open {
		v.fail(dialogProblem(textNoProject))
		return ResetReport{}, false
	}
	report, err := a.ResetKey(ctx, p.ID, newPassword, typed)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return ResetReport{}, false
	}
	v.finish(func(s *State) {
		s.Project = v.currentProject()
		s.Project.Report = &report
		s.Notice = NoticeKeyReset
	}, nil)
	return report, true
}

func (v *ViewModel) Logout(ctx context.Context) AccountOutcome {
	return v.leave(func(a Account) error { return a.Logout(ctx) })
}

func (v *ViewModel) RemoveDevice(ctx context.Context) AccountOutcome {
	return v.leave(func(a Account) error { return a.RemoveDevice(ctx) })
}

func (v *ViewModel) leave(call func(Account) error) AccountOutcome {
	v.begin()
	a := v.snapshot().Account
	if a == nil {
		v.fail(dialogProblem(textNotAvailable))
		return AccountFailed
	}
	if err := call(a); err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return AccountFailed
	}
	v.finish(func(*State) {}, nil)
	return AccountDone
}

func wipe(buffers ...[]byte) {
	for _, b := range buffers {
		clear(b)
	}
}
