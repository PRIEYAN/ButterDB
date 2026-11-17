package vm

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

type Project = embedded.Project

type ProjectID = embedded.ProjectID

type ResetReport = embedded.ResetReport

const (
	MinPasswordChars  = embedded.MinPasswordChars
	MaxNameChars      = embedded.MaxProjectNameChars
	ResetConfirmation = embedded.ResetConfirmation
)

type Mode string

const (
	ModeList   Mode = "list"
	ModeCreate Mode = "create"
	ModeUnlock Mode = "unlock"
	ModeReset  Mode = "reset"
	ModeReport Mode = "report"
)

type Action string

const (
	ActionNone   Action = ""
	ActionOpen   Action = "open"
	ActionUnlock Action = "unlock"
	ActionFinish Action = "finish"
)

type Row struct {
	Project Project
	Badge   string
	Action  Action
}

type State struct {
	Loading bool
	Loaded  bool
	Rows    []Row
	Mode    Mode
	Target  Project
	Busy    string
	Problem *model.Problem
	Report  *ResetReport
}

type ViewModel struct {
	projects embedded.Projects
	now      func() time.Time
	mu       sync.Mutex
	state    *model.Observable[State]
}

func New(p embedded.Projects, now func() time.Time) *ViewModel {
	if now == nil {
		now = time.Now
	}
	return &ViewModel{projects: p, now: now, state: model.NewObservable(State{Mode: ModeList})}
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Enter(ctx context.Context) {
	var closeErr error
	if _, open := v.projects.Current(); open {
		closeErr = v.projects.CloseProject(ctx)
	}
	v.update(func(s *State) {
		s.Mode = ModeList
		s.Report = nil
		s.Busy = ""
		s.Target = Project{}
	})
	v.Refresh(ctx)
	if closeErr != nil {
		problem := v.describe(closeErr)
		v.update(func(s *State) { s.Problem = problem })
	}
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.update(func(s *State) {
		s.Loading = true
		s.Problem = nil
	})
	list, err := v.projects.List(ctx)
	problem := v.describe(err)
	v.update(func(s *State) {
		s.Loading = false
		if err != nil {
			s.Problem = problem
			return
		}
		s.Loaded = true
		s.Rows = rowsFor(list)
	})
}

func rowsFor(list []Project) []Row {
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b Project) int {
		if c := b.LastOpened.Compare(a.LastOpened); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	rows := make([]Row, 0, len(sorted))
	for _, p := range sorted {
		badge, action := describeState(p.State)
		rows = append(rows, Row{Project: p, Badge: badge, Action: action})
	}
	return rows
}

func describeState(state embedded.ProjectState) (string, Action) {
	switch state {
	case embedded.ProjectReady:
		return "Ready", ActionOpen
	case embedded.ProjectLocked:
		return "Needs password on this device", ActionUnlock
	case embedded.ProjectLegacy:
		return "Created outside the app", ActionNone
	case embedded.ProjectTooNew:
		return "Needs a newer butterDB", ActionNone
	case embedded.ProjectUnfinished:
		return "Finish setup", ActionFinish
	}
	return string(state), ActionNone
}

func (v *ViewModel) StartCreate() {
	v.update(func(s *State) {
		s.Mode = ModeCreate
		s.Problem = nil
		s.Target = Project{}
	})
}

func (v *ViewModel) StartUnlock(id ProjectID) {
	v.toTarget(id, ModeUnlock)
}

func (v *ViewModel) StartReset(id ProjectID) {
	v.toTarget(id, ModeReset)
}

func (v *ViewModel) toTarget(id ProjectID, mode Mode) {
	v.update(func(s *State) {
		s.Problem = nil
		for _, row := range s.Rows {
			if row.Project.ID == id {
				s.Target = row.Project
			}
		}
		if s.Target.ID != id {
			s.Target = Project{ID: id}
		}
		s.Mode = mode
	})
}

func (v *ViewModel) Back() {
	v.update(func(s *State) {
		s.Mode = ModeList
		s.Problem = nil
		s.Report = nil
		s.Target = Project{}
	})
}

func (v *ViewModel) Create(ctx context.Context, name string, password, confirm []byte) (Project, bool) {
	defer wipe(password, confirm)
	name = strings.TrimSpace(name)
	if problem := model.CheckProjectName(name); problem != nil {
		v.update(func(s *State) { s.Problem = problem })
		return Project{}, false
	}
	if problem := model.CheckNewPassword(password, confirm); problem != nil {
		v.update(func(s *State) { s.Problem = problem })
		return Project{}, false
	}
	v.begin("Creating " + name + "…")
	created, err := v.projects.Create(ctx, name, password)
	if err != nil {
		v.fail(err)
		return Project{}, false
	}
	v.update(func(s *State) { s.Target = created })
	return v.open(ctx, created)
}

func (v *ViewModel) Unlock(ctx context.Context, password []byte) (Project, bool) {
	defer wipe(password)
	target := v.state.Get().Target
	if len(password) == 0 {
		v.update(func(s *State) {
			s.Problem = ptr(model.InlineProblem(model.FieldPassword, textEnterPassword))
		})
		return Project{}, false
	}
	v.begin("Unlocking " + target.Name + "…")
	if err := v.projects.Unlock(ctx, target.ID, password); err != nil {
		v.fail(err)
		return Project{}, false
	}
	return v.open(ctx, target)
}

func (v *ViewModel) Open(ctx context.Context, id ProjectID) (Project, bool) {
	target := Project{ID: id}
	for _, row := range v.state.Get().Rows {
		if row.Project.ID == id {
			target = row.Project
		}
	}
	v.update(func(s *State) { s.Target = target })
	return v.open(ctx, target)
}

func (v *ViewModel) OpenTarget(ctx context.Context) (Project, bool) {
	return v.open(ctx, v.state.Get().Target)
}

func (v *ViewModel) open(ctx context.Context, target Project) (Project, bool) {
	v.begin(openingText(target.Name))
	opened, err := v.projects.Open(ctx, target.ID)
	if err != nil {
		v.fail(err)
		return Project{}, false
	}
	v.update(func(s *State) {
		s.Busy = ""
		s.Target = opened
	})
	return opened, true
}

func openingText(name string) string {
	if name == "" {
		return "Opening project…"
	}
	return "Opening " + name + "…"
}

func (v *ViewModel) Reset(ctx context.Context, password, confirm []byte, typed string) (ResetReport, bool) {
	defer wipe(password, confirm)
	target := v.state.Get().Target
	if problem := model.CheckNewPassword(password, confirm); problem != nil {
		v.update(func(s *State) { s.Problem = problem })
		return ResetReport{}, false
	}
	if problem := model.CheckResetConfirmation(typed); problem != nil {
		v.update(func(s *State) { s.Problem = problem })
		return ResetReport{}, false
	}
	v.begin("Resetting the key of " + target.Name + "…")
	report, err := v.projects.ResetKey(ctx, target.ID, password, typed)
	if err != nil {
		v.fail(err)
		return ResetReport{}, false
	}
	v.update(func(s *State) {
		s.Busy = ""
		s.Mode = ModeReport
		s.Report = &report
	})
	return report, true
}

func (v *ViewModel) begin(busy string) {
	v.update(func(s *State) {
		s.Busy = busy
		s.Problem = nil
	})
}

func (v *ViewModel) fail(err error) {
	problem := v.describe(err)
	v.update(func(s *State) {
		s.Busy = ""
		s.Problem = problem
	})
}

func (v *ViewModel) update(fn func(*State)) {
	v.mu.Lock()
	s := v.state.Get()
	fn(&s)
	v.state.Set(s)
	v.mu.Unlock()
}

func (v *ViewModel) describe(err error) *model.Problem {
	if err == nil {
		return nil
	}
	p := model.Describe(err, v.now())
	if p.Title == "" && p.Code == "" {
		return nil
	}
	return &p
}

const textEnterPassword = "Enter the project password."

func wipe(buffers ...[]byte) {
	for _, b := range buffers {
		clear(b)
	}
}

func ptr(p model.Problem) *model.Problem {
	return &p
}
