package projects

import (
	"context"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/projects/vm"
)

type fakeProjects struct {
	mu      sync.Mutex
	list    []vm.Project
	creates []string
	opened  []vm.ProjectID
}

func (f *fakeProjects) List(context.Context) ([]vm.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vm.Project(nil), f.list...), nil
}

func (f *fakeProjects) Create(_ context.Context, name string, _ []byte) (vm.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, name)
	p := vm.Project{ID: "new", Name: name, State: "ready"}
	f.list = append(f.list, p)
	return p, nil
}

func (f *fakeProjects) Unlock(context.Context, vm.ProjectID, []byte) error { return nil }

func (f *fakeProjects) Open(_ context.Context, id vm.ProjectID) (vm.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, id)
	for _, p := range f.list {
		if p.ID == id {
			return p, nil
		}
	}
	return vm.Project{ID: id}, nil
}

func (f *fakeProjects) Current() (vm.Project, bool) { return vm.Project{}, false }

func (f *fakeProjects) CloseProject(context.Context) error { return nil }

func (f *fakeProjects) Rename(context.Context, vm.ProjectID, string) error { return nil }

func (f *fakeProjects) ChangePassword(context.Context, vm.ProjectID, []byte, []byte) error {
	return nil
}

func (f *fakeProjects) ResetKey(context.Context, vm.ProjectID, []byte, string) (vm.ResetReport, error) {
	return vm.ResetReport{}, nil
}

func (f *fakeProjects) Logout(context.Context) error { return nil }

func (f *fakeProjects) RemoveDevice(context.Context) error { return nil }

func (f *fakeProjects) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func visible(o fyne.CanvasObject) []fyne.CanvasObject {
	if o == nil || !o.Visible() {
		return nil
	}
	out := []fyne.CanvasObject{o}
	switch c := o.(type) {
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(c).Objects() {
			out = append(out, visible(child)...)
		}
	case *fyne.Container:
		for _, child := range c.Objects {
			out = append(out, visible(child)...)
		}
	}
	return out
}

func findEntry(t *testing.T, root fyne.CanvasObject, placeholder string) *widget.Entry {
	t.Helper()
	for _, o := range visible(root) {
		if e, ok := o.(*widget.Entry); ok && e.PlaceHolder == placeholder {
			return e
		}
	}
	t.Fatalf("entry %q not on screen", placeholder)
	return nil
}

func findButton(t *testing.T, root fyne.CanvasObject, text string) *widget.Button {
	t.Helper()
	for _, o := range visible(root) {
		if b, ok := o.(*widget.Button); ok && b.Text == text {
			return b
		}
	}
	t.Fatalf("button %q not on screen", text)
	return nil
}

type uiQueue chan func()

func (q uiQueue) drain() {
	for {
		select {
		case f := <-q:
			f()
		default:
			return
		}
	}
}

func (q uiQueue) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.drain()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCreateFormRefusesBadPasswordsAndClearsThem(t *testing.T) {
	test.NewTempApp(t)
	fake := &fakeProjects{}
	v := vm.New(fake, nil)
	var opened []vm.Project
	var mu sync.Mutex
	screen := NewScreen(v, nil, func(p vm.Project) {
		mu.Lock()
		opened = append(opened, p)
		mu.Unlock()
	})
	queue := make(uiQueue, 256)
	screen.view.post = func(f func()) { queue <- f }
	test.NewTempWindow(t, screen.Content())
	root := screen.Content()
	v.Refresh(context.Background())
	queue.drain()

	test.Tap(findButton(t, root, "New project"))
	queue.drain()
	cases := []struct {
		name, password, confirm, want string
	}{
		{"short", "short", "short", "Use at least 10 characters."},
		{"mismatch", "long enough pass", "long enough pasS", "The two passwords do not match."},
	}
	for _, tc := range cases {
		findEntry(t, root, PlaceholderName).SetText("photos")
		findEntry(t, root, PlaceholderPassword).SetText(tc.password)
		findEntry(t, root, PlaceholderConfirm).SetText(tc.confirm)
		test.Tap(findButton(t, root, "Create project"))
		if got := findEntry(t, root, PlaceholderPassword).Text + findEntry(t, root, PlaceholderConfirm).Text; got != "" {
			t.Fatalf("%s: password fields kept %q after submit", tc.name, got)
		}
		queue.waitFor(t, tc.name+" error", func() bool {
			p := v.State().Get().Problem
			return p != nil && p.Presentation == model.PresentInline && p.Title == tc.want &&
				screen.view.create.errText.Visible() && screen.view.create.errText.Text == tc.want
		})
		if fake.createCount() != 0 {
			t.Fatalf("%s: a project was created", tc.name)
		}
	}

	findEntry(t, root, PlaceholderPassword).SetText("long enough pass")
	findEntry(t, root, PlaceholderConfirm).SetText("long enough pass")
	test.Tap(findButton(t, root, "Create project"))
	if findEntry(t, root, PlaceholderPassword).Text != "" || findEntry(t, root, PlaceholderConfirm).Text != "" {
		t.Fatalf("password fields kept their text after a good submit")
	}
	queue.waitFor(t, "project opened", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(opened) == 1
	})
	if opened[0].Name != "photos" || fake.createCount() != 1 {
		t.Fatalf("opened %+v after %d creates", opened, fake.createCount())
	}
}
