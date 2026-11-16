package vm

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type ViewModel struct {
	flow    LoginFlow
	now     func() time.Time
	state   *model.Observable[State]
	polling atomic.Bool

	mu           sync.Mutex
	override     Step
	overrideFrom adminv1.TelegramNext
	lastNext     adminv1.TelegramNext
}

func New(flow LoginFlow, now func() time.Time) *ViewModel {
	return &ViewModel{
		flow:  flow,
		now:   now,
		state: model.NewObservable(State{Loading: true, Step: StepWait}),
	}
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func (v *ViewModel) Interval() time.Duration {
	if v.state.Get().Step == StepWait {
		return WaitPollInterval
	}
	return IdlePollInterval
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.begin()
	st, err := v.flow.Status(ctx)
	v.settle(ctx, st, err)
}

func (v *ViewModel) Poll(ctx context.Context) {
	if !v.polling.CompareAndSwap(false, true) {
		return
	}
	defer v.polling.Store(false)
	if v.state.Get().Loading {
		return
	}
	st, err := v.flow.Status(ctx)
	if err != nil {
		v.raisePollFailure(err)
		return
	}
	v.update(func(s *State) { v.project(s, st) })
}

func (v *ViewModel) Tick() {
	v.update(func(s *State) {
		if s.SubmitDisabled && !v.now().Before(s.FloodUntil) {
			s.SubmitDisabled = false
			s.FloodUntil = time.Time{}
		}
	})
}

func (v *ViewModel) SaveApp(ctx context.Context, apiID, apiHash string) {
	id, ok := parseAPIID(apiID)
	if !ok {
		v.reject(FieldAPIID, reasonAPIID)
		return
	}
	hash, ok := normaliseAPIHash(apiHash)
	if !ok {
		v.reject(FieldAPIHash, reasonAPIHash)
		return
	}
	v.begin()
	st, err := v.flow.SaveApp(ctx, adminv1.TelegramCredentialsRequest{APIID: id, APIHash: hash})
	v.settle(ctx, st, err)
}

func (v *ViewModel) ChangeApp(ctx context.Context) {
	v.begin()
	st, err := v.flow.DeleteApp(ctx)
	v.settle(ctx, st, err)
}

func (v *ViewModel) StartLogin(ctx context.Context, phone string) {
	if v.state.Get().SubmitDisabled {
		return
	}
	number, ok := normalisePhone(phone)
	if !ok {
		v.reject(FieldPhone, reasonPhone)
		return
	}
	v.begin()
	st, err := v.flow.Start(ctx, number)
	v.settle(ctx, st, err)
}

func (v *ViewModel) SubmitCode(ctx context.Context, code string) {
	code = strings.TrimSpace(code)
	if code == "" {
		v.reject(FieldCode, reasonCode)
		return
	}
	if v.state.Get().SubmitDisabled {
		return
	}
	loginID, ok := v.loginID()
	if !ok {
		v.Refresh(ctx)
		return
	}
	v.begin()
	st, err := v.flow.SubmitCode(ctx, loginID, code)
	v.settle(ctx, st, err)
}

func (v *ViewModel) SubmitPassword(ctx context.Context, password string) {
	if password == "" {
		v.reject(FieldPassword, reasonPassword)
		return
	}
	if v.state.Get().SubmitDisabled {
		return
	}
	loginID, ok := v.loginID()
	if !ok {
		v.Refresh(ctx)
		return
	}
	v.begin()
	st, err := v.flow.SubmitPassword(ctx, loginID, password)
	v.settle(ctx, st, err)
}

func (v *ViewModel) Cancel(ctx context.Context) {
	v.begin()
	st, err := v.flow.Cancel(ctx)
	v.settle(ctx, st, err)
}

func (v *ViewModel) loginID() (string, bool) {
	s := v.state.Get()
	if s.Loading || s.Login == nil || s.Login.LoginID == "" {
		return "", false
	}
	return s.Login.LoginID, true
}

func (v *ViewModel) begin() {
	v.update(func(s *State) { s.Loading = true })
}

func (v *ViewModel) settle(ctx context.Context, st adminv1.TelegramStatus, err error) {
	if err != nil {
		v.fail(ctx, err)
		return
	}
	v.update(func(s *State) {
		s.Loading = false
		v.override = ""
		v.project(s, st)
	})
}

func (v *ViewModel) reject(field Field, reason string) {
	p := v.localProblem(field, reason)
	v.update(func(s *State) {
		s.Loading = false
		v.raise(s, p)
		v.focus(s, field)
	})
}

func (v *ViewModel) update(change func(*State)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.state.Get()
	change(&s)
	v.state.Set(s)
}

func (v *ViewModel) project(s *State, st adminv1.TelegramStatus) {
	s.Credentials = st.Credentials
	s.Login = st.Login
	s.Account = st.Account
	v.lastNext = st.Next
	step, ok := stepForNext(st.Next)
	if !ok {
		return
	}
	if v.override != "" {
		if st.Next == v.overrideFrom {
			step = v.override
		} else {
			v.override = ""
		}
	}
	v.moveTo(s, step)
}

func (v *ViewModel) moveTo(s *State, step Step) {
	if s.Step == step {
		return
	}
	s.Step = step
	s.Problem = nil
	v.focus(s, firstField(step))
}

func (v *ViewModel) focus(s *State, field Field) {
	if field == FieldNone {
		return
	}
	s.Focus = field
	s.FocusSeq++
}

func (v *ViewModel) raise(s *State, p model.Problem) {
	s.Problem = &p
	s.ProblemSeq++
}

func (v *ViewModel) localProblem(field Field, reason string) model.Problem {
	return model.Describe(&adminv1.APIError{
		Status: 422,
		Body: adminv1.ErrorBody{
			Code:    adminv1.CodeValidationFailed,
			Details: map[string]any{"field": string(field), "reason": reason},
		},
	}, v.now())
}

func (v *ViewModel) raisePollFailure(err error) {
	p := model.Describe(err, v.now())
	if p.Presentation == "" {
		return
	}
	v.update(func(s *State) { v.raise(s, p) })
}
