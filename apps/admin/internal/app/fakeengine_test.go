package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

type fakeEngine struct {
	admin  *fakeAdmin
	target *url.URL
	now    time.Time

	trips atomic.Int32

	mu        sync.Mutex
	projects  []embedded.Project
	passwords map[embedded.ProjectID]string
	current   *embedded.Project
	closes    int
	removes   int
}

func newFakeEngine(admin *fakeAdmin) *fakeEngine {
	target, err := url.Parse(admin.srv.URL)
	if err != nil {
		admin.t.Fatalf("parse fake admin url: %v", err)
	}
	return &fakeEngine{admin: admin, target: target, now: admin.now, passwords: map[embedded.ProjectID]string{}}
}

func (e *fakeEngine) RoundTrip(r *http.Request) (*http.Response, error) {
	e.trips.Add(1)
	out := r.Clone(r.Context())
	out.URL.Scheme = e.target.Scheme
	out.URL.Host = e.target.Host
	out.Host = ""
	return http.DefaultTransport.RoundTrip(out)
}

func (e *fakeEngine) seedProject(name string, state embedded.ProjectState, password string) embedded.Project {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := embedded.Project{ID: embedded.ProjectID("p" + strconv.Itoa(len(e.projects)+1)), Name: name, State: state, CreatedAt: e.now}
	e.projects = append(e.projects, p)
	e.passwords[p.ID] = password
	return p
}

func (e *fakeEngine) find(id embedded.ProjectID) int {
	for i, p := range e.projects {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (e *fakeEngine) List(context.Context) ([]embedded.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]embedded.Project(nil), e.projects...), nil
}

func (e *fakeEngine) Create(_ context.Context, name string, password []byte) (embedded.Project, error) {
	if len(password) < embedded.MinPasswordChars {
		return embedded.Project{}, embedded.ErrPasswordTooShort
	}
	return e.seedProject(name, embedded.ProjectReady, string(password)), nil
}

func (e *fakeEngine) Unlock(_ context.Context, id embedded.ProjectID, password []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.find(id)
	if i < 0 {
		return embedded.ErrProjectNotFound
	}
	if e.passwords[id] != string(password) {
		return embedded.ErrWrongPassword
	}
	e.projects[i].State = embedded.ProjectReady
	return nil
}

func (e *fakeEngine) Open(_ context.Context, id embedded.ProjectID) (embedded.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.find(id)
	if i < 0 {
		return embedded.Project{}, embedded.ErrProjectNotFound
	}
	if s := e.projects[i].State; s != embedded.ProjectReady && s != embedded.ProjectUnfinished {
		return embedded.Project{}, embedded.ErrProjectNotReady
	}
	e.projects[i].State = embedded.ProjectReady
	e.projects[i].LastOpened = e.now
	p := e.projects[i]
	e.current = &p
	return p, nil
}

func (e *fakeEngine) Current() (embedded.Project, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == nil {
		return embedded.Project{}, false
	}
	return *e.current, true
}

func (e *fakeEngine) isOpen() bool {
	_, open := e.Current()
	return open
}

func (e *fakeEngine) closeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closes
}

func (e *fakeEngine) CloseProject(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil {
		e.closes++
	}
	e.current = nil
	return nil
}

func (e *fakeEngine) Rename(_ context.Context, id embedded.ProjectID, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.find(id)
	if i < 0 {
		return embedded.ErrProjectNotFound
	}
	e.projects[i].Name = name
	if e.current != nil && e.current.ID == id {
		e.current.Name = name
	}
	return nil
}

func (e *fakeEngine) ChangePassword(_ context.Context, id embedded.ProjectID, oldPassword, newPassword []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.passwords[id] != string(oldPassword) {
		return embedded.ErrWrongPassword
	}
	e.passwords[id] = string(newPassword)
	return nil
}

func (e *fakeEngine) ResetKey(_ context.Context, id embedded.ProjectID, newPassword []byte, confirmation string) (embedded.ResetReport, error) {
	if confirmation != embedded.ResetConfirmation {
		return embedded.ResetReport{}, embedded.ErrConfirmation
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.passwords[id] = string(newPassword)
	return embedded.ResetReport{UnreadableObjects: map[string]int64{}}, nil
}

func (e *fakeEngine) Logout(ctx context.Context) error {
	_ = e.CloseProject(ctx)
	e.admin.logout()
	return nil
}

func (e *fakeEngine) RemoveDevice(ctx context.Context) error {
	e.mu.Lock()
	e.removes++
	e.mu.Unlock()
	return e.Logout(ctx)
}
