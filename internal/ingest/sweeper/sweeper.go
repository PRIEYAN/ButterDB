package sweeper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const (
	DefaultInterval          = time.Minute
	DefaultRecoveryAge       = 5 * time.Minute
	DefaultMaintenanceEvery  = time.Hour
	DefaultTerminalRetention = 7 * 24 * time.Hour
	DefaultBatch             = 100
	resumeBackoffBase        = time.Minute
	resumeBackoffCap         = time.Hour
	resumeBackoffMaxShift    = 20
)

type Config struct {
	Interval          time.Duration
	RecoveryAge       time.Duration
	MaintenanceEvery  time.Duration
	TerminalRetention time.Duration
	OrphanGrace       time.Duration
	Batch             int
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.RecoveryAge <= 0 {
		c.RecoveryAge = DefaultRecoveryAge
	}
	if c.MaintenanceEvery <= 0 {
		c.MaintenanceEvery = DefaultMaintenanceEvery
	}
	if c.TerminalRetention <= 0 {
		c.TerminalRetention = DefaultTerminalRetention
	}
	if c.OrphanGrace <= 0 {
		c.OrphanGrace = limits.OrphanBodyGrace
	}
	if c.Batch <= 0 {
		c.Batch = DefaultBatch
	}
	return c
}

type Dependencies struct {
	Jobs     JobStore
	Uploads  UploadResumer
	Deletes  DeleteResumer
	Due      DueCleanup
	Drainer  ingest.CleanupDrainer
	Orphans  OrphanBodies
	Sleeper  Sleeper
	Clock    clock.Clock
	Logger   *slog.Logger
	Settings Config
}

type retryState struct {
	failures int
	notUntil time.Time
}

type Sweeper struct {
	deps Dependencies
	cfg  Config

	mu              sync.Mutex
	lastMaintenance time.Time
	retries         map[string]retryState

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	started     bool
}

func New(deps Dependencies) (*Sweeper, error) {
	missing := []struct {
		name string
		nil  bool
	}{
		{"jobs", deps.Jobs == nil},
		{"uploads", deps.Uploads == nil},
		{"deletes", deps.Deletes == nil},
		{"due cleanup", deps.Due == nil},
		{"drainer", deps.Drainer == nil},
		{"orphans", deps.Orphans == nil},
		{"sleeper", deps.Sleeper == nil},
		{"clock", deps.Clock == nil},
		{"logger", deps.Logger == nil},
	}
	for _, dep := range missing {
		if dep.nil {
			return nil, fmt.Errorf("sweeper: %w: %s", errs.ErrMissingDependency, dep.name)
		}
	}
	return &Sweeper{deps: deps, cfg: deps.Settings.withDefaults(), retries: make(map[string]retryState)}, nil
}

func (s *Sweeper) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started {
		return errors.New("sweeper: already started")
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	go s.loop(runCtx, s.done)
	return nil
}

func (s *Sweeper) Close(ctx context.Context) error {
	s.lifecycleMu.Lock()
	cancel, done := s.cancel, s.done
	s.lifecycleMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sweeper: close: %w", ctx.Err())
	}
}

func (s *Sweeper) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	s.pass(ctx, 0)
	for {
		if err := s.deps.Sleeper.Sleep(ctx, s.cfg.Interval); err != nil {
			return
		}
		s.pass(ctx, s.cfg.RecoveryAge)
	}
}

func (s *Sweeper) RunOnce(ctx context.Context) {
	s.pass(ctx, s.cfg.RecoveryAge)
}

func (s *Sweeper) pass(ctx context.Context, recoveryAge time.Duration) {
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"resume uploads", func(ctx context.Context) error { return s.resumeUploads(ctx, recoveryAge) }},
		{"resume deletes", func(ctx context.Context) error { return s.resumeDeletes(ctx, recoveryAge) }},
		{"drain cleanup", s.drainCleanup},
		{"maintenance", s.maintain},
	}
	for _, step := range steps {
		if ctx.Err() != nil {
			return
		}
		if err := step.run(ctx); err != nil && ctx.Err() == nil {
			s.deps.Logger.Warn("sweeper step failed", "step", step.name, "error", err)
		}
	}
}

func (s *Sweeper) resumeUploads(ctx context.Context, recoveryAge time.Duration) error {
	now := s.deps.Clock.Now()
	jobs, err := s.deps.Jobs.ListRecoverableUploads(ctx, now.Add(-recoveryAge).UnixMilli(), s.cfg.Batch+s.backedOffCount("upload"))
	if err != nil {
		return err
	}
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.JobID
	}
	s.forgetStale("upload", ids, len(jobs) < s.cfg.Batch)
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil
		}
		s.resume(ctx, "upload", id, s.deps.Uploads.Resume)
	}
	return nil
}

func (s *Sweeper) resumeDeletes(ctx context.Context, recoveryAge time.Duration) error {
	now := s.deps.Clock.Now()
	jobs, err := s.deps.Jobs.ListRecoverableDeletes(ctx, now.Add(-recoveryAge).UnixMilli(), s.cfg.Batch+s.backedOffCount("delete"))
	if err != nil {
		return err
	}
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.JobID
	}
	s.forgetStale("delete", ids, len(jobs) < s.cfg.Batch)
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil
		}
		s.resume(ctx, "delete", id, s.deps.Deletes.Resume)
	}
	return nil
}

func retryKey(kind, jobID string) string {
	return kind + ":" + jobID
}

func (s *Sweeper) resume(ctx context.Context, kind, jobID string, resume func(context.Context, string) error) {
	key := retryKey(kind, jobID)
	if !s.due(key) {
		return
	}
	err := resume(ctx, jobID)
	if err == nil {
		s.clearRetry(key)
		return
	}
	if ctx.Err() != nil {
		return
	}
	s.recordFailure(key)
	s.deps.Logger.Warn("sweeper resume failed", "kind", kind, "job", jobID, "code", errs.Code(err))
}

func (s *Sweeper) due(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, found := s.retries[key]
	return !found || !s.deps.Clock.Now().Before(state.notUntil)
}

func (s *Sweeper) recordFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.retries[key]
	delay := min(resumeBackoffBase<<min(state.failures, resumeBackoffMaxShift), resumeBackoffCap)
	state.failures++
	state.notUntil = s.deps.Clock.Now().Add(delay)
	s.retries[key] = state
}

func (s *Sweeper) clearRetry(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.retries, key)
}

func (s *Sweeper) backedOffCount(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for key := range s.retries {
		if strings.HasPrefix(key, kind+":") {
			count++
		}
	}
	return count
}

func (s *Sweeper) forgetStale(kind string, listed []string, complete bool) {
	if !complete {
		return
	}
	present := make(map[string]struct{}, len(listed))
	for _, id := range listed {
		present[retryKey(kind, id)] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.retries {
		if _, found := present[key]; !found && strings.HasPrefix(key, kind+":") {
			delete(s.retries, key)
		}
	}
}

func (s *Sweeper) drainCleanup(ctx context.Context) error {
	channels, err := s.deps.Due.DueCleanupChannels(ctx, s.deps.Clock.Now().UnixMilli(), s.cfg.Batch)
	if err != nil {
		return err
	}
	for _, channelID := range channels {
		if ctx.Err() != nil {
			return nil
		}
		if err := s.deps.Drainer.DrainChannel(ctx, channelID); err != nil && ctx.Err() == nil {
			s.deps.Logger.Warn("sweeper cleanup drain failed", "channel", channelID, "code", errs.Code(err))
		}
	}
	return nil
}

func (s *Sweeper) maintain(ctx context.Context) error {
	now := s.deps.Clock.Now()
	s.mu.Lock()
	skip := !s.lastMaintenance.IsZero() && now.Sub(s.lastMaintenance) < s.cfg.MaintenanceEvery
	if !skip {
		s.lastMaintenance = now
	}
	s.mu.Unlock()
	if skip {
		return nil
	}
	var failures error
	if _, err := s.deps.Jobs.DeleteExpiredIdempotency(ctx, now.UnixMilli()); err != nil {
		failures = errors.Join(failures, fmt.Errorf("expire idempotency: %w", err))
	}
	if _, err := s.deps.Jobs.PruneTerminalJobs(ctx, now.Add(-s.cfg.TerminalRetention).UnixMilli()); err != nil {
		failures = errors.Join(failures, fmt.Errorf("prune terminal jobs: %w", err))
	}
	if err := s.queueOrphanBodies(ctx, now); err != nil {
		failures = errors.Join(failures, fmt.Errorf("orphan bodies: %w", err))
	}
	return failures
}

func (s *Sweeper) queueOrphanBodies(ctx context.Context, now time.Time) error {
	channels, err := s.deps.Orphans.BodyChannels(ctx)
	if err != nil {
		return err
	}
	cutoff := now.Add(-s.cfg.OrphanGrace).Unix()
	for _, channelID := range channels {
		if err := s.queueChannelOrphans(ctx, channelID, cutoff); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sweeper) queueChannelOrphans(ctx context.Context, channelID, cutoff int64) error {
	var after int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msgIDs, jobIDs, err := s.deps.Orphans.OrphanBodies(ctx, channelID, cutoff, after, s.cfg.Batch)
		if err != nil {
			return err
		}
		if len(msgIDs) == 0 {
			return nil
		}
		after = msgIDs[len(msgIDs)-1]
		unowned, err := s.withoutOpenJob(ctx, msgIDs, jobIDs)
		if err != nil {
			return err
		}
		if len(unowned) == 0 {
			continue
		}
		if _, err := s.deps.Orphans.QueueOrphanBodies(ctx, channelID, unowned); err != nil {
			return err
		}
	}
}

func (s *Sweeper) withoutOpenJob(ctx context.Context, msgIDs []int64, jobIDs []string) ([]int64, error) {
	open, err := s.deps.Jobs.OpenUploadJobIDs(ctx, jobIDs)
	if err != nil {
		return nil, err
	}
	unowned := make([]int64, 0, len(msgIDs))
	for i, msgID := range msgIDs {
		if _, owned := open[jobIDs[i]]; !owned {
			unowned = append(unowned, msgID)
		}
	}
	return unowned, nil
}
