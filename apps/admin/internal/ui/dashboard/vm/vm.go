package vm

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	PollInterval       = 15 * time.Second
	ReplicaUnavailable = "not_available"
)

var errNoClient = errors.New("dashboard: no client")

type Client interface {
	Status(ctx context.Context) (adminv1.Status, error)
	Usage(ctx context.Context) (adminv1.Usage, error)
}

type Waiter func(d time.Duration) (<-chan time.Time, func())

type Option func(*ViewModel)

func WithPollInterval(d time.Duration) Option {
	return func(v *ViewModel) {
		if d > 0 {
			v.interval = d
		}
	}
}

func WithWaiter(w Waiter) Option {
	return func(v *ViewModel) {
		if w != nil {
			v.wait = w
		}
	}
}

type State struct {
	Loading     bool
	Problem     *model.Problem
	HasStatus   bool
	HasUsage    bool
	Status      adminv1.Status
	Usage       adminv1.Usage
	RefreshedAt time.Time
}

type ViewModel struct {
	client   Client
	now      func() time.Time
	state    *model.Observable[State]
	interval time.Duration
	wait     Waiter

	applyMu sync.Mutex

	pollMu sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func New(c Client, now func() time.Time, opts ...Option) *ViewModel {
	if now == nil {
		now = time.Now
	}
	v := &ViewModel{
		client:   c,
		now:      now,
		state:    model.NewObservable(State{}),
		interval: PollInterval,
		wait:     timerWaiter,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

func timerWaiter(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.refresh(ctx)
}

func (v *ViewModel) refresh(ctx context.Context) *model.Problem {
	v.markLoading()
	status, statusErr := v.fetchStatus(ctx)
	usage, usageErr := v.fetchUsage(ctx)
	return v.apply(status, statusErr, usage, usageErr)
}

func (v *ViewModel) fetchStatus(ctx context.Context) (adminv1.Status, error) {
	if v.client == nil {
		return adminv1.Status{}, errNoClient
	}
	return v.client.Status(ctx)
}

func (v *ViewModel) fetchUsage(ctx context.Context) (adminv1.Usage, error) {
	if v.client == nil {
		return adminv1.Usage{}, errNoClient
	}
	return v.client.Usage(ctx)
}

func (v *ViewModel) markLoading() {
	v.applyMu.Lock()
	s := v.state.Get()
	s.Loading = true
	v.applyMu.Unlock()
	v.state.Set(s)
}

func (v *ViewModel) apply(status adminv1.Status, statusErr error, usage adminv1.Usage, usageErr error) *model.Problem {
	now := v.now()
	v.applyMu.Lock()
	s := v.state.Get()
	s.Loading = false
	s.Problem = firstProblem(now, statusErr, usageErr)
	if statusErr == nil {
		s.Status, s.HasStatus = status, true
	}
	if usageErr == nil {
		s.Usage, s.HasUsage = usage, true
	}
	if statusErr == nil || usageErr == nil {
		s.RefreshedAt = now
	}
	v.applyMu.Unlock()
	v.state.Set(s)
	return s.Problem
}

func firstProblem(now time.Time, errs ...error) *model.Problem {
	for _, err := range errs {
		if err == nil {
			continue
		}
		p := model.Describe(err, now)
		if p.Title == "" && p.Code == "" {
			continue
		}
		return &p
	}
	return nil
}

func (v *ViewModel) Start(ctx context.Context) {
	v.Stop()
	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	v.pollMu.Lock()
	v.cancel, v.done = cancel, done
	v.pollMu.Unlock()
	go v.poll(pollCtx, done)
}

func (v *ViewModel) Stop() {
	v.pollMu.Lock()
	cancel, done := v.cancel, v.done
	v.cancel, v.done = nil, nil
	v.pollMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (v *ViewModel) poll(ctx context.Context, done chan struct{}) {
	defer close(done)
	failures := 0
	for {
		problem := v.refresh(ctx)
		if ctx.Err() != nil || endsPolling(problem) {
			return
		}
		failures = nextFailureCount(failures, problem)
		ticks, stop := v.wait(v.delay(failures))
		select {
		case <-ctx.Done():
			stop()
			return
		case <-ticks:
		}
	}
}

func endsPolling(p *model.Problem) bool {
	return p != nil && p.Presentation == model.PresentRoute
}

func nextFailureCount(failures int, p *model.Problem) int {
	if p != nil && p.Retryable {
		return failures + 1
	}
	return 0
}

func (v *ViewModel) delay(failures int) time.Duration {
	if failures == 0 {
		return v.interval
	}
	return backoffDelay(failures)
}

func backoffDelay(failures int) time.Duration {
	switch failures {
	case 1:
		return 2 * time.Second
	case 2:
		return 4 * time.Second
	case 3:
		return 8 * time.Second
	case 4:
		return 16 * time.Second
	}
	return 30 * time.Second
}
