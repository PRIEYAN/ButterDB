package vm

import (
	"context"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

type Client interface {
	Status(ctx context.Context) (adminv1.Status, error)
	TelegramStatus(ctx context.Context) (adminv1.TelegramStatus, error)
	Audit(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error)
}

type Account interface {
	Current() (embedded.Project, bool)
	Rename(ctx context.Context, id embedded.ProjectID, name string) error
	ChangePassword(ctx context.Context, id embedded.ProjectID, oldPassword, newPassword []byte) error
	ResetKey(ctx context.Context, id embedded.ProjectID, newPassword []byte, confirmation string) (embedded.ResetReport, error)
	Logout(ctx context.Context) error
	RemoveDevice(ctx context.Context) error
}

type Preferences interface {
	StringWithFallback(key, fallback string) string
	SetString(key, value string)
	IntWithFallback(key string, fallback int) int
	SetInt(key string, value int)
}

type Session struct {
	Account            Account
	Preferences        Preferences
	AppVersion         string
	DefaultDownloadDir string
}

type State struct {
	Loading     bool
	Problem     *model.Problem
	Notice      string
	Project     ProjectState
	Telegram    TelegramState
	Activity    ActivityState
	About       AboutState
	Preferences PreferencesState
}

type ViewModel struct {
	client Client
	now    func() time.Time
	state  *model.Observable[State]

	mu      sync.Mutex
	session Session

	stateMu sync.Mutex
}

func New(c Client, now func() time.Time, s Session) *ViewModel {
	if now == nil {
		now = time.Now
	}
	v := &ViewModel{client: c, now: now, session: s, state: model.NewObservable(State{})}
	v.state.Set(State{
		Project:     v.currentProject(),
		Preferences: v.readPreferences(),
		About:       aboutStatic(s.AppVersion, ""),
	})
	return v
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.begin()
	status, statusErr := v.client.Status(ctx)
	telegram, telegramErr := v.client.TelegramStatus(ctx)
	v.finish(func(s *State) {
		s.Project = v.currentProject()
		if statusErr == nil {
			s.About = aboutStatic(v.snapshot().AppVersion, status.ServerVersion)
		}
		if telegramErr == nil {
			s.Telegram = TelegramState{Loaded: true, Status: telegram}
		}
		s.Preferences = v.readPreferences()
	}, v.problemOf(statusErr, telegramErr))
}

func (v *ViewModel) snapshot() Session {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.session
}

func (v *ViewModel) begin() {
	v.mutate(func(s *State) {
		s.Loading = true
		s.Problem = nil
		s.Notice = ""
	})
}

func (v *ViewModel) finish(apply func(*State), problem *model.Problem) {
	v.mutate(func(s *State) {
		apply(s)
		s.Loading = false
		s.Problem = problem
	})
}

func (v *ViewModel) fail(problem model.Problem) {
	v.finish(func(*State) {}, &problem)
}

func (v *ViewModel) mutate(fn func(*State)) {
	v.stateMu.Lock()
	s := v.state.Get()
	fn(&s)
	v.stateMu.Unlock()
	v.state.Set(s)
}

func (v *ViewModel) problemOf(errs ...error) *model.Problem {
	for _, err := range errs {
		if err == nil {
			continue
		}
		p := model.Describe(err, v.now())
		if p.Title == "" && p.Code == "" {
			continue
		}
		return &p
	}
	return nil
}
