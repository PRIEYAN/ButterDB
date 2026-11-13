package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) record(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
}

func (j *journal) snapshot() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.events...)
}

type fakeComponent struct {
	name     string
	journal  *journal
	startErr error
	closeErr error
	onClose  func(ctx context.Context) error
}

func (f *fakeComponent) Start(context.Context) error {
	f.journal.record("start " + f.name)
	return f.startErr
}

func (f *fakeComponent) Close(ctx context.Context) error {
	f.journal.record("close " + f.name)
	if f.onClose != nil {
		return f.onClose(ctx)
	}
	return f.closeErr
}

func addNamed(g *Group, j *journal, names ...string) []*fakeComponent {
	var added []*fakeComponent
	for _, name := range names {
		c := &fakeComponent{name: name, journal: j}
		g.Add(name, c)
		added = append(added, c)
	}
	return added
}

func TestGroupStartsInOrder(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		want  []string
	}{
		{"empty group", nil, nil},
		{"single component", []string{"a"}, []string{"start a"}},
		{"three components", []string{"a", "b", "c"}, []string{"start a", "start b", "start c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &journal{}
			g := NewGroup(time.Second)
			addNamed(g, j, tc.names...)
			if err := g.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if got := j.snapshot(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGroupClosesInReverse(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		want  []string
	}{
		{"single component", []string{"a"}, []string{"start a", "close a"}},
		{"three components", []string{"a", "b", "c"}, []string{"start a", "start b", "start c", "close c", "close b", "close a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &journal{}
			g := NewGroup(time.Second)
			addNamed(g, j, tc.names...)
			if err := g.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := g.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := j.snapshot(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
			if err := g.Close(context.Background()); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			if got := j.snapshot(); len(got) != len(tc.want) {
				t.Fatalf("second Close closed again: %v", got)
			}
		})
	}
}

func TestStartFailureClosesStarted(t *testing.T) {
	sentinel := errors.New("boom")
	cases := []struct {
		name     string
		failAt   int
		closeErr error
		want     []string
	}{
		{"first fails", 0, nil, []string{"start a"}},
		{"middle fails", 1, nil, []string{"start a", "start b", "close a"}},
		{"last fails", 2, nil, []string{"start a", "start b", "start c", "close b", "close a"}},
		{"close failure does not stop rollback", 2, errors.New("close failed"), []string{"start a", "start b", "start c", "close b", "close a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &journal{}
			g := NewGroup(time.Second)
			components := addNamed(g, j, "a", "b", "c")
			addNamed(g, j, "d")
			components[tc.failAt].startErr = sentinel
			for _, c := range components[:tc.failAt] {
				c.closeErr = tc.closeErr
			}
			err := g.Start(context.Background())
			if !errors.Is(err, sentinel) {
				t.Fatalf("Start error = %v, want wrapping %v", err, sentinel)
			}
			if tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
				t.Fatalf("Start error = %v, want it to carry %v", err, tc.closeErr)
			}
			if got := j.snapshot(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
			if err := g.Close(context.Background()); err != nil {
				t.Fatalf("Close after failed Start: %v", err)
			}
			if got := j.snapshot(); len(got) != len(tc.want) {
				t.Fatalf("Close after rollback closed again: %v", got)
			}
		})
	}
}

type cancelingComponent struct {
	fakeComponent
	cancel context.CancelFunc
}

func (c *cancelingComponent) Start(ctx context.Context) error {
	c.cancel()
	return c.fakeComponent.Start(ctx)
}

func TestStartFailureClosesStartedWhenContextCanceled(t *testing.T) {
	j := &journal{}
	g := NewGroup(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	closeCtxErr := errors.New("close not called")
	first := &cancelingComponent{
		fakeComponent: fakeComponent{name: "a", journal: j, onClose: func(ctx context.Context) error {
			closeCtxErr = ctx.Err()
			return nil
		}},
		cancel: cancel,
	}
	second := &fakeComponent{name: "b", journal: j}
	g.Add("a", first)
	g.Add("b", second)
	err := g.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want canceled", err)
	}
	if closeCtxErr != nil {
		t.Fatalf("close context error = %v, want nil", closeCtxErr)
	}
	want := []string{"start a", "close a"}
	if got := j.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestCloseBoundedByContext(t *testing.T) {
	j := &journal{}
	g := NewGroup(20 * time.Millisecond)
	components := addNamed(g, j, "a", "b", "c")
	blocked := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	components[2].onClose = blocked
	components[1].onClose = blocked
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	begin := time.Now()
	err := g.Close(context.Background())
	elapsed := time.Since(begin)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline exceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Close took %s, close timeout was not applied", elapsed)
	}
	want := []string{"start a", "start b", "start c", "close c", "close b", "close a"}
	if got := j.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestNewGroupDefaultsNonPositiveTimeout(t *testing.T) {
	j := &journal{}
	g := NewGroup(0)
	var deadlineSet bool
	c := &fakeComponent{name: "a", journal: j, onClose: func(ctx context.Context) error {
		_, deadlineSet = ctx.Deadline()
		return nil
	}}
	g.Add("a", c)
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !deadlineSet {
		t.Fatal("close context had no deadline")
	}
}

func TestAddRejectsMisuse(t *testing.T) {
	t.Run("nil component", func(t *testing.T) {
		g := NewGroup(time.Second)
		g.Add("a", nil)
		if err := g.Start(context.Background()); !errors.Is(err, errs.ErrMissingDependency) {
			t.Fatalf("Start error = %v, want missing dependency", err)
		}
	})
	t.Run("start twice", func(t *testing.T) {
		g := NewGroup(time.Second)
		if err := g.Start(context.Background()); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		if err := g.Start(context.Background()); err == nil {
			t.Fatal("second Start succeeded")
		}
	})
}
