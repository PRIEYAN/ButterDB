package vm

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClient struct {
	mu          sync.Mutex
	status      adminv1.Status
	usage       adminv1.Usage
	statusErr   error
	usageErr    error
	statusCalls int
	usageCalls  int
	called      chan struct{}
}

func newFakeClient() *fakeClient {
	return &fakeClient{called: make(chan struct{}, 64)}
}

func (f *fakeClient) Status(ctx context.Context) (adminv1.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	select {
	case f.called <- struct{}{}:
	default:
	}
	return f.status, f.statusErr
}

func (f *fakeClient) Usage(ctx context.Context) (adminv1.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usageCalls++
	return f.usage, f.usageErr
}

func (f *fakeClient) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusCalls, f.usageCalls
}

func (f *fakeClient) setErrors(statusErr, usageErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusErr, f.usageErr = statusErr, usageErr
}

type fakeWaiter struct {
	mu       sync.Mutex
	delays   []time.Duration
	ticks    chan time.Time
	requests chan time.Duration
}

func newFakeWaiter() *fakeWaiter {
	return &fakeWaiter{ticks: make(chan time.Time), requests: make(chan time.Duration, 64)}
}

func (w *fakeWaiter) wait(d time.Duration) (<-chan time.Time, func()) {
	w.mu.Lock()
	w.delays = append(w.delays, d)
	w.mu.Unlock()
	w.requests <- d
	return w.ticks, func() {}
}

func (w *fakeWaiter) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.delays...)
}

func newViewModel(c Client, w *fakeWaiter) *ViewModel {
	return New(c, func() time.Time { return testNow }, WithWaiter(w.wait))
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for channel")
		panic("unreachable")
	}
}

func TestStatusAndUsageMerged(t *testing.T) {
	linkedAt := testNow.Add(-time.Hour)
	until := testNow.Add(time.Minute)
	tests := []struct {
		name      string
		statusErr error
		usageErr  error
		check     func(t *testing.T, s State)
	}{
		{
			name: "both succeed into one state",
			check: func(t *testing.T, s State) {
				if !s.HasStatus || !s.HasUsage || s.Problem != nil || s.Loading {
					t.Fatalf("state = %+v", s)
				}
				if s.Status.ServerVersion != "0.1.0" || s.Usage.Objects != 18234 {
					t.Fatalf("merged data = %+v / %+v", s.Status, s.Usage)
				}
				if !s.RefreshedAt.Equal(testNow) {
					t.Fatalf("RefreshedAt = %v", s.RefreshedAt)
				}
				if !s.FloodWaitActive(testNow) {
					t.Fatalf("flood wait should be active")
				}
				if got := s.CacheFraction(); got != 0.5 {
					t.Fatalf("CacheFraction = %v", got)
				}
				if got := s.SpoolFraction(); got != 0 {
					t.Fatalf("SpoolFraction = %v", got)
				}
				if !s.ReplicaVisible() {
					t.Fatalf("replica should be visible")
				}
			},
		},
		{
			name:     "usage failure keeps status and reports problem",
			usageErr: &adminv1.APIError{Status: 500, Body: adminv1.ErrorBody{Code: adminv1.CodeInternal, RequestID: "req1"}},
			check: func(t *testing.T, s State) {
				if !s.HasStatus || s.HasUsage {
					t.Fatalf("HasStatus=%v HasUsage=%v", s.HasStatus, s.HasUsage)
				}
				if s.Problem == nil || s.Problem.Code != adminv1.CodeInternal || s.Problem.RequestID != "req1" {
					t.Fatalf("Problem = %+v", s.Problem)
				}
			},
		},
		{
			name:      "status failure keeps usage",
			statusErr: &net.OpError{Op: "dial", Err: errors.New("refused")},
			check: func(t *testing.T, s State) {
				if s.HasStatus || !s.HasUsage {
					t.Fatalf("HasStatus=%v HasUsage=%v", s.HasStatus, s.HasUsage)
				}
				if s.Problem == nil || !s.Problem.Retryable || s.Problem.Presentation != model.PresentBanner {
					t.Fatalf("Problem = %+v", s.Problem)
				}
			},
		},
		{
			name:      "cancelled context leaves no problem",
			statusErr: context.Canceled,
			usageErr:  context.Canceled,
			check: func(t *testing.T, s State) {
				if s.Problem != nil || s.HasStatus || s.HasUsage || s.Loading {
					t.Fatalf("state = %+v", s)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeClient()
			c.status = adminv1.Status{
				ServerVersion: "0.1.0",
				State:         "ready",
				Telegram:      adminv1.TelegramHealth{Linked: true, FloodWaitUntil: &until, Account: &adminv1.TelegramAccount{LinkedAt: linkedAt}},
				Replica:       adminv1.ReplicaSummary{State: "not_linked"},
			}
			c.usage = adminv1.Usage{Objects: 18234, BlockCache: adminv1.CacheUsage{UsedBytes: 5, LimitBytes: 10}}
			c.setErrors(tt.statusErr, tt.usageErr)
			v := New(c, func() time.Time { return testNow })
			v.Refresh(context.Background())
			tt.check(t, v.State().Get())
		})
	}
}

func TestStatusAndUsageMergedKeepsLastGoodHalf(t *testing.T) {
	c := newFakeClient()
	c.status = adminv1.Status{ServerVersion: "0.1.0"}
	c.usage = adminv1.Usage{Objects: 7}
	v := New(c, func() time.Time { return testNow })
	v.Refresh(context.Background())
	c.setErrors(nil, &adminv1.APIError{Status: 500, Body: adminv1.ErrorBody{Code: adminv1.CodeInternal}})
	c.mu.Lock()
	c.status = adminv1.Status{ServerVersion: "0.2.0"}
	c.mu.Unlock()
	v.Refresh(context.Background())
	s := v.State().Get()
	if s.Status.ServerVersion != "0.2.0" || s.Usage.Objects != 7 || s.Problem == nil {
		t.Fatalf("state = %+v", s)
	}
}

func TestDerivedValues(t *testing.T) {
	tests := []struct {
		name  string
		state State
		check func(State) bool
	}{
		{"cache full clamps to one", State{Usage: adminv1.Usage{BlockCache: adminv1.CacheUsage{UsedBytes: 20, LimitBytes: 10}}}, func(s State) bool { return s.CacheFraction() == 1 }},
		{"zero limit is zero", State{Usage: adminv1.Usage{Spool: adminv1.SpoolUsage{UsedBytes: 20}}}, func(s State) bool { return s.SpoolFraction() == 0 }},
		{"replica hidden when not available", State{HasStatus: true, Status: adminv1.Status{Replica: adminv1.ReplicaSummary{State: "not_available"}}}, func(s State) bool { return !s.ReplicaVisible() }},
		{"replica hidden before data", State{}, func(s State) bool { return !s.ReplicaVisible() }},
		{"flood wait in the past is inactive", State{HasStatus: true, Status: adminv1.Status{Telegram: adminv1.TelegramHealth{FloodWaitUntil: ptrTime(testNow.Add(-time.Second))}}}, func(s State) bool { return !s.FloodWaitActive(testNow) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.check(tt.state) {
				t.Fatalf("unexpected derived value for %+v", tt.state)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestPollStopsOnHide(t *testing.T) {
	c := newFakeClient()
	w := newFakeWaiter()
	v := newViewModel(c, w)

	v.Start(context.Background())
	if d := receive(t, w.requests); d != PollInterval {
		t.Fatalf("first wait = %v, want %v", d, PollInterval)
	}
	if s, u := c.counts(); s != 1 || u != 1 {
		t.Fatalf("calls after show = %d/%d", s, u)
	}

	w.ticks <- testNow
	receive(t, w.requests)
	if s, u := c.counts(); s != 2 || u != 2 {
		t.Fatalf("calls after tick = %d/%d", s, u)
	}

	v.Stop()
	select {
	case w.ticks <- testNow:
		t.Fatalf("poll goroutine still receiving after Stop")
	default:
	}
	if s, _ := c.counts(); s != 2 {
		t.Fatalf("status calls after Stop = %d", s)
	}
	v.Stop()
}

func TestPollStopsWhenContextCancelled(t *testing.T) {
	c := newFakeClient()
	w := newFakeWaiter()
	v := newViewModel(c, w)
	ctx, cancel := context.WithCancel(context.Background())
	v.Start(ctx)
	receive(t, w.requests)
	cancel()
	v.Stop()
	if s, _ := c.counts(); s != 1 {
		t.Fatalf("status calls = %d", s)
	}
}

func TestStartRestartsPolling(t *testing.T) {
	c := newFakeClient()
	w := newFakeWaiter()
	v := newViewModel(c, w)
	v.Start(context.Background())
	receive(t, w.requests)
	v.Start(context.Background())
	receive(t, w.requests)
	if s, _ := c.counts(); s != 2 {
		t.Fatalf("status calls = %d, want an immediate refresh per Start", s)
	}
	v.Stop()
}

func TestPollBacksOffOnTransportFailure(t *testing.T) {
	c := newFakeClient()
	c.setErrors(&net.OpError{Op: "dial", Err: errors.New("refused")}, nil)
	w := newFakeWaiter()
	v := newViewModel(c, w)
	v.Start(context.Background())
	for range 6 {
		receive(t, w.requests)
		w.ticks <- testNow
	}
	receive(t, w.requests)
	v.Stop()
	got := w.recorded()[:7]
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays = %v, want %v", got, want)
		}
	}

	c.setErrors(nil, nil)
	v2 := newViewModel(c, newFakeWaiter())
	v2.Refresh(context.Background())
	if v2.State().Get().Problem != nil {
		t.Fatalf("problem not cleared after recovery")
	}
}

func TestPollStopsOnRouteProblem(t *testing.T) {
	c := newFakeClient()
	c.setErrors(&adminv1.APIError{Status: 401, Body: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated}}, nil)
	w := newFakeWaiter()
	v := newViewModel(c, w)
	v.Start(context.Background())
	v.Stop()
	if s, _ := c.counts(); s != 1 {
		t.Fatalf("status calls = %d", s)
	}
	select {
	case d := <-w.requests:
		t.Fatalf("waited %v after a route problem", d)
	default:
	}
	if p := v.State().Get().Problem; p == nil || p.Presentation != model.PresentRoute || p.Route != model.RouteConnect {
		t.Fatalf("Problem = %+v", p)
	}
}

func TestNilClientReportsProblem(t *testing.T) {
	v := New(nil, func() time.Time { return testNow })
	v.Refresh(context.Background())
	if v.State().Get().Problem == nil {
		t.Fatalf("expected a problem without a client")
	}
}

func TestObserversSeeLoading(t *testing.T) {
	c := newFakeClient()
	v := New(c, func() time.Time { return testNow })
	var seen []bool
	cancel := v.State().Subscribe(func(s State) { seen = append(seen, s.Loading) })
	defer cancel()
	v.Refresh(context.Background())
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("loading sequence = %v", seen)
	}
}
