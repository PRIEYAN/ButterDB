package core

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const (
	usageCacheTTL         = 30 * time.Second
	maxPresignSeconds     = 7 * 24 * 60 * 60
	stateUnlinked         = "unlinked"
	stateSyncing          = "syncing"
	stateReady            = "ready"
	stateDegraded         = "degraded"
	componentOK           = "ok"
	componentDown         = "down"
	replicaNotAvailable   = "not_available"
	encryptedObjectsQuery = `SELECT COUNT(*) FROM objects WHERE encrypted = 1`
)

var _ handlers.StatusSource = (*adminStatus)(nil)

type adminStatusDependencies struct {
	config    config.Config
	relay     setup.LoginRelay
	slot      *BackendSlot
	clock     clock.Clock
	serverID  string
	build     BuildInfo
	startedAt time.Time
}

type adminStatus struct {
	deps adminStatusDependencies

	mu       sync.Mutex
	cached   adminv1.Usage
	cachedAt time.Time
}

func newAdminStatus(deps adminStatusDependencies) *adminStatus {
	return &adminStatus{deps: deps}
}

func (s *adminStatus) Status(ctx context.Context) (adminv1.Status, error) {
	now := s.deps.clock.Now()
	snap, err := s.deps.relay.Status(ctx)
	if err != nil {
		return adminv1.Status{}, fmt.Errorf("core: admin status: telegram: %w", err)
	}
	backend := s.deps.slot.Current()
	cfg := s.deps.config
	return adminv1.Status{
		ServerID:      s.deps.serverID,
		ServerVersion: s.deps.build.Version,
		Commit:        s.deps.build.Commit,
		StartedAt:     s.deps.startedAt.UTC(),
		UptimeSeconds: int64(now.Sub(s.deps.startedAt) / time.Second),
		State:         serverState(snap, backend),
		Telegram:      telegramHealth(snap, backend, cfg.Telegram.SendRate.ControlPerMinute),
		Components:    s.components(backend),
		Endpoints:     adminv1.Endpoints{S3PublicURL: cfg.S3.PublicURL, S3Region: cfg.S3.Region, MediaPublicURL: mediaPublicURL(cfg.Media)},
		Limits: adminv1.Limits{
			MaxObjectBytes: limits.MaxStoredObjectBytes, MaxKeyBytes: limits.MaxKeyBytes, MaxUserMetadataBytes: limits.MaxUserMetadataBytes,
			MaxPresignSeconds: maxPresignSeconds, MaxBuckets: limits.MaxBuckets, MaxDeleteBatch: limits.MaxDeleteObjectsKeys,
		},
		Replica: adminv1.ReplicaSummary{State: replicaNotAvailable},
	}, nil
}

func serverState(snap identity.Snapshot, backend *Backend) string {
	switch {
	case snap.State != identity.StateLinked:
		return stateUnlinked
	case backend == nil:
		return stateSyncing
	case backend.Readiness == nil:
		return stateReady
	}
	switch backend.Readiness.State() {
	case router.StateReady:
		return stateReady
	case router.StateDegraded:
		return stateDegraded
	}
	return stateSyncing
}

func telegramHealth(snap identity.Snapshot, backend *Backend, sendRate int) adminv1.TelegramHealth {
	health := adminv1.TelegramHealth{
		Linked:            snap.State == identity.StateLinked,
		Connected:         backend != nil,
		SendRatePerMinute: sendRate,
	}
	if snap.Account != nil {
		health.Account = &adminv1.TelegramAccount{
			UserID:      snap.Account.UserID,
			Username:    snap.Account.Username,
			FirstName:   snap.Account.FirstName,
			PhoneMasked: snap.Account.PhoneMasked,
			LinkedAt:    snap.Account.LinkedAt.UTC(),
			Connected:   backend != nil,
		}
	}
	return health
}

func mediaPublicURL(cfg config.MediaConfig) string {
	if !cfg.Enabled {
		return ""
	}
	return cfg.PublicURL
}

func (s *adminStatus) components(backend *Backend) []adminv1.Component {
	status := componentDown
	if backend != nil {
		status = componentOK
	}
	names := []string{"telegram", "projection", "sync", "block_cache", "spool", "s3_listener"}
	components := make([]adminv1.Component, 0, len(names))
	for _, name := range names {
		components = append(components, adminv1.Component{Name: name, Status: status, Since: s.deps.startedAt.UTC()})
	}
	return components
}

func (s *adminStatus) Usage(ctx context.Context) (adminv1.Usage, error) {
	backend := s.deps.slot.Current()
	if backend == nil {
		return adminv1.Usage{}, s.unavailable(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.deps.clock.Now()
	if !s.cachedAt.IsZero() && now.Sub(s.cachedAt) < usageCacheTTL {
		return s.cached, nil
	}
	usage, err := s.compute(ctx, backend, now)
	if err != nil {
		return adminv1.Usage{}, err
	}
	s.cached, s.cachedAt = usage, now
	return usage, nil
}

func (s *adminStatus) unavailable(ctx context.Context) error {
	return backendGate{slot: s.deps.slot, relay: s.deps.relay}.unavailable(ctx)
}

func (s *adminStatus) compute(ctx context.Context, backend *Backend, now time.Time) (adminv1.Usage, error) {
	infos, err := backend.Sync.Buckets.List(ctx)
	if err != nil {
		return adminv1.Usage{}, fmt.Errorf("core: admin usage: %w", err)
	}
	usage := adminv1.Usage{Buckets: len(infos), ComputedAt: now.UTC()}
	for _, info := range infos {
		usage.Objects += info.ObjectCount
		usage.StoredBytes += info.StoredBytes
		usage.PlaintextBytes += info.PlaintextBytes
	}
	if usage.EncryptedObjects, err = countEncryptedObjects(ctx, backend); err != nil {
		return adminv1.Usage{}, err
	}
	stats := backend.Read.Cache.Stats()
	usage.BlockCache = adminv1.CacheUsage{UsedBytes: stats.UsedBytes, LimitBytes: stats.LimitBytes, HitRatio1h: hitRatio(stats.Hits, stats.Misses)}
	usage.Spool = adminv1.SpoolUsage{UsedBytes: backend.Write.Spool.UsedBytes(), LimitBytes: int64(s.deps.config.Spool.MaxBytes)}
	return usage, nil
}

func countEncryptedObjects(ctx context.Context, backend *Backend) (int64, error) {
	if backend.Database == nil {
		return 0, nil
	}
	var count int64
	err := backend.Database.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, encryptedObjectsQuery).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("core: admin usage: count encrypted objects: %w", err)
	}
	return count, nil
}

func hitRatio(hits, misses int64) float64 {
	if hits+misses == 0 {
		return 0
	}
	return float64(hits) / float64(hits+misses)
}
