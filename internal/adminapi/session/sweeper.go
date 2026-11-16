package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	DefaultDeviceIdleRevoke = 180 * 24 * time.Hour
	DefaultRevokedRetention = 30 * 24 * time.Hour
	DefaultAuditMaxAge      = 90 * 24 * time.Hour
	DefaultAuditMaxRows     = 100000
	DefaultSweepInterval    = time.Hour
)

type SweepStore interface {
	RevokeIdle(ctx context.Context, before time.Time) (int, error)
	DeleteRevokedBefore(ctx context.Context, cutoff time.Time) (int, error)
	DeleteExpiredSecrets(ctx context.Context, now time.Time) (int, error)
	PruneAudit(ctx context.Context, olderThan time.Time, maxRows int) (int, error)
}

type SweeperConfig struct {
	IdleRevoke       time.Duration
	RevokedRetention time.Duration
	AuditMaxAge      time.Duration
	AuditMaxRows     int
}

type SweeperDependencies struct {
	Store  SweepStore
	Audit  AuditWriter
	Clock  clock.Clock
	Logger *slog.Logger
	Config SweeperConfig
}

type Sweeper struct {
	deps SweeperDependencies
}

func NewSweeper(deps SweeperDependencies) (*Sweeper, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("session: %w: sweep store", errs.ErrMissingDependency)
	}
	if deps.Audit == nil {
		return nil, fmt.Errorf("session: %w: audit writer", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("session: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("session: %w: logger", errs.ErrMissingDependency)
	}
	deps.Config = deps.Config.withDefaults()
	return &Sweeper{deps: deps}, nil
}

func (c SweeperConfig) withDefaults() SweeperConfig {
	if c.IdleRevoke <= 0 {
		c.IdleRevoke = DefaultDeviceIdleRevoke
	}
	if c.RevokedRetention <= 0 {
		c.RevokedRetention = DefaultRevokedRetention
	}
	if c.AuditMaxAge <= 0 {
		c.AuditMaxAge = DefaultAuditMaxAge
	}
	if c.AuditMaxRows <= 0 {
		c.AuditMaxRows = DefaultAuditMaxRows
	}
	return c
}

func (s *Sweeper) RunOnce(ctx context.Context) error {
	now := s.deps.Clock.Now()
	cfg := s.deps.Config
	var failures []error
	if err := s.revokeIdle(ctx, now); err != nil {
		failures = append(failures, err)
	}
	if _, err := s.deps.Store.DeleteRevokedBefore(ctx, now.Add(-cfg.RevokedRetention)); err != nil {
		failures = append(failures, fmt.Errorf("session: purge revoked devices: %w", err))
	}
	if _, err := s.deps.Store.DeleteExpiredSecrets(ctx, now); err != nil {
		failures = append(failures, fmt.Errorf("session: delete expired secrets: %w", err))
	}
	if _, err := s.deps.Store.PruneAudit(ctx, now.Add(-cfg.AuditMaxAge), cfg.AuditMaxRows); err != nil {
		failures = append(failures, fmt.Errorf("session: prune audit: %w", err))
	}
	return errors.Join(failures...)
}

func (s *Sweeper) revokeIdle(ctx context.Context, now time.Time) error {
	revoked, err := s.deps.Store.RevokeIdle(ctx, now.Add(-s.deps.Config.IdleRevoke))
	if err != nil {
		return fmt.Errorf("session: revoke idle devices: %w", err)
	}
	if revoked == 0 {
		return nil
	}
	entry := adminrepo.AuditEntry{
		At:        now,
		ActorType: "system",
		Action:    "device.idle_revoke",
		Target:    strconv.Itoa(revoked),
		Outcome:   adminrepo.AuditOutcomeSuccess,
	}
	if _, err := s.deps.Audit.InsertAudit(ctx, entry); err != nil {
		return fmt.Errorf("session: audit idle revoke: %w", err)
	}
	return nil
}

func (s *Sweeper) Run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, open := <-ticks:
			if !open {
				return nil
			}
			if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
				s.deps.Logger.Warn("admin sweep failed", "error", err)
			}
		}
	}
}
