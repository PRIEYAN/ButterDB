package keys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/keyrepo"
)

const (
	lastUsedInterval  = 60 * time.Second
	maxCreateAttempts = 4
)

type Repository interface {
	Insert(ctx context.Context, rec keyrepo.Record) error
	Get(ctx context.Context, id access.AccessKeyID) (keyrepo.Record, error)
	List(ctx context.Context, includeRevoked bool) ([]keyrepo.Record, error)
	Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope, now time.Time) (keyrepo.Record, error)
	Revoke(ctx context.Context, id access.AccessKeyID, now time.Time) (keyrepo.Record, error)
	TouchLastUsed(ctx context.Context, id access.AccessKeyID, now time.Time) error
}

type Sealer interface {
	SealSecret(accessKeyID string, secret []byte) ([]byte, error)
	OpenSecret(accessKeyID string, sealed []byte) ([]byte, error)
}

type Dependencies struct {
	Repo   Repository
	Sealer Sealer
	Clock  clock.Clock
	Random io.Reader
}

type Service struct {
	deps  Dependencies
	cache *recordCache

	touchMu sync.Mutex
	touched map[access.AccessKeyID]time.Time
}

func NewService(deps Dependencies) (*Service, error) {
	if deps.Repo == nil {
		return nil, fmt.Errorf("keys: %w: repo", errs.ErrMissingDependency)
	}
	if deps.Sealer == nil {
		return nil, fmt.Errorf("keys: %w: sealer", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("keys: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Random == nil {
		return nil, fmt.Errorf("keys: %w: random", errs.ErrMissingDependency)
	}
	return &Service{
		deps:    deps,
		cache:   newRecordCache(cacheCapacity),
		touched: make(map[access.AccessKeyID]time.Time),
	}, nil
}

func (s *Service) List(ctx context.Context, includeRevoked bool) ([]access.KeyInfo, error) {
	records, err := s.deps.Repo.List(ctx, includeRevoked)
	if err != nil {
		return nil, fmt.Errorf("keys: list: %w", err)
	}
	infos := make([]access.KeyInfo, 0, len(records))
	for _, rec := range records {
		infos = append(infos, infoOf(rec))
	}
	return infos, nil
}

func (s *Service) Create(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error) {
	if err := access.ValidateKeyName(name); err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", err)
	}
	if err := requireScope(scope); err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", err)
	}
	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		info, secret, err := s.createOnce(ctx, name, scope)
		if errors.Is(err, keyrepo.ErrDuplicateKey) {
			continue
		}
		return info, secret, err
	}
	return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", keyrepo.ErrDuplicateKey)
}

func (s *Service) createOnce(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error) {
	id, err := generateID(s.deps.Random)
	if err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", err)
	}
	secret, err := generateSecret(s.deps.Random)
	if err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", err)
	}
	defer clear(secret)
	sealed, err := s.deps.Sealer.SealSecret(id.String(), secret)
	if err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: seal secret: %w", err)
	}
	nowMillis := s.deps.Clock.Now().UnixMilli()
	rec := keyrepo.Record{
		ID:           id,
		Name:         name,
		SecretSealed: sealed,
		Scope:        scope,
		CreatedAt:    nowMillis,
		UpdatedAt:    nowMillis,
	}
	if err := s.deps.Repo.Insert(ctx, rec); err != nil {
		return access.KeyInfo{}, "", fmt.Errorf("keys: create: %w", err)
	}
	return infoOf(rec), string(secret), nil
}

func (s *Service) Get(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error) {
	if err := id.Validate(); err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: get: %w", errs.ErrAccessKeyNotFound)
	}
	rec, err := s.deps.Repo.Get(ctx, id)
	if err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: get: %w", err)
	}
	return infoOf(rec), nil
}

func (s *Service) Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error) {
	if err := id.Validate(); err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: update: %w", errs.ErrAccessKeyNotFound)
	}
	if name != nil {
		if err := access.ValidateKeyName(*name); err != nil {
			return access.KeyInfo{}, fmt.Errorf("keys: update: %w", err)
		}
	}
	if scope != nil {
		if err := requireScope(*scope); err != nil {
			return access.KeyInfo{}, fmt.Errorf("keys: update: %w", err)
		}
	}
	rec, err := s.deps.Repo.Update(ctx, id, name, scope, s.deps.Clock.Now())
	s.cache.invalidate(id)
	if err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: update: %w", err)
	}
	return infoOf(rec), nil
}

func (s *Service) Revoke(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error) {
	if err := id.Validate(); err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: revoke: %w", errs.ErrAccessKeyNotFound)
	}
	rec, err := s.deps.Repo.Revoke(ctx, id, s.deps.Clock.Now())
	s.cache.invalidate(id)
	s.forgetTouch(id)
	if err != nil {
		return access.KeyInfo{}, fmt.Errorf("keys: revoke: %w", err)
	}
	return infoOf(rec), nil
}

func (s *Service) Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error) {
	rec, err := s.lookup(ctx, id)
	if err != nil {
		return nil, access.KeyInfo{}, fmt.Errorf("keys: secret: %w", err)
	}
	info := infoOf(rec)
	if info.Revoked() {
		return nil, info, fmt.Errorf("keys: secret: %w", errs.ErrAccessKeyRevoked)
	}
	secret, err := s.deps.Sealer.OpenSecret(id.String(), rec.SecretSealed)
	if err != nil {
		return nil, info, fmt.Errorf("keys: secret: unseal: %w", err)
	}
	s.recordUse(ctx, id)
	return secret, info, nil
}

func (s *Service) lookup(ctx context.Context, id access.AccessKeyID) (keyrepo.Record, error) {
	if err := id.Validate(); err != nil {
		return keyrepo.Record{}, errs.ErrAccessKeyNotFound
	}
	if rec, ok := s.cache.get(id); ok {
		return rec, nil
	}
	epoch := s.cache.currentEpoch()
	rec, err := s.deps.Repo.Get(ctx, id)
	if err != nil {
		return keyrepo.Record{}, err
	}
	s.cache.putIfEpoch(rec, epoch)
	return rec, nil
}

func (s *Service) recordUse(ctx context.Context, id access.AccessKeyID) {
	now := s.deps.Clock.Now()
	if !s.claimTouch(id, now) {
		return
	}
	// A failed last_used_at write must never fail authentication.
	_ = s.deps.Repo.TouchLastUsed(ctx, id, now)
}

func (s *Service) claimTouch(id access.AccessKeyID, now time.Time) bool {
	s.touchMu.Lock()
	defer s.touchMu.Unlock()
	if last, ok := s.touched[id]; ok && now.Sub(last) < lastUsedInterval {
		return false
	}
	s.touched[id] = now
	return true
}

func (s *Service) forgetTouch(id access.AccessKeyID) {
	s.touchMu.Lock()
	defer s.touchMu.Unlock()
	delete(s.touched, id)
}

func requireScope(scope access.Scope) error {
	if len(scope.Buckets()) == 0 || len(scope.Actions()) == 0 {
		return fmt.Errorf("%w: scope is empty", access.ErrInvalidScope)
	}
	return nil
}

func infoOf(rec keyrepo.Record) access.KeyInfo {
	return access.KeyInfo{
		ID:         rec.ID,
		Name:       rec.Name,
		Scope:      rec.Scope,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
		LastUsedAt: rec.LastUsedAt,
		RevokedAt:  rec.RevokedAt,
	}
}
