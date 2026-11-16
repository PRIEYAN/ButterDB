package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	adminAPIVersion    = "v1"
	pairingMethodHost  = "host"
	pairingMethodSaved = "saved_messages"
)

var (
	_ setup.ClaimStore         = claimStore{}
	_ handlers.SetupEndpoints  = (*setupEndpoints)(nil)
	_ setup.LoginRelay         = (*limitedRelay)(nil)
	_ session.ClaimStateSource = claimStore{}
)

type claimRepository interface {
	ClaimState(ctx context.Context) (string, error)
	ReplaceSetupToken(ctx context.Context, hash [32]byte, expiresAt time.Time) error
	ConsumeSetupTokenAndAddDevice(ctx context.Context, hash [32]byte, now time.Time, dev adminrepo.NewDevice) error
}

type claimStore struct {
	repo claimRepository
}

func (c claimStore) ClaimState(ctx context.Context) (string, error) {
	return c.repo.ClaimState(ctx)
}

func (c claimStore) ReplaceSetupToken(ctx context.Context, hash [32]byte, expiresAt time.Time) error {
	return c.repo.ReplaceSetupToken(ctx, hash, expiresAt)
}

func (c claimStore) ConsumeSetupTokenAndAddDevice(ctx context.Context, hash [32]byte, now time.Time, dev adminrepo.NewDevice) error {
	err := c.repo.ConsumeSetupTokenAndAddDevice(ctx, hash, now, dev)
	switch {
	case errors.Is(err, adminrepo.ErrAlreadyClaimed):
		return fmt.Errorf("core: claim: %w: %w", setup.ErrAlreadyClaimed, err)
	case errors.Is(err, adminrepo.ErrSetupTokenInvalid):
		return fmt.Errorf("core: claim: %w: %w", setup.ErrSetupTokenInvalid, err)
	}
	return err
}

type tlsReporter interface {
	info() adminv1.TLSInfo
}

type setupEndpointsDependencies struct {
	service  *setup.Service
	claims   claimStore
	relay    setup.LoginRelay
	limiter  *session.Limiter
	clock    clock.Clock
	logger   *slog.Logger
	serverID string
	build    BuildInfo
	tls      tlsReporter
}

type setupEndpoints struct {
	deps setupEndpointsDependencies
}

func newSetupEndpoints(deps setupEndpointsDependencies) *setupEndpoints {
	return &setupEndpoints{deps: deps}
}

func (s *setupEndpoints) Relay() setup.LoginRelay {
	return s.deps.service.Relay()
}

func (s *setupEndpoints) Claim(ctx context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error) {
	if err := s.deps.limiter.Take(session.ScopeClaim, device.RemoteAddr); err != nil {
		return adminv1.ClaimResult{}, translateAdminError(err)
	}
	result, err := s.deps.service.Claim(ctx, token, device)
	if err != nil {
		return adminv1.ClaimResult{}, translateAdminError(err)
	}
	return result, nil
}

func (s *setupEndpoints) ServerInfo(ctx context.Context) (adminv1.ServerInfo, error) {
	if err := s.deps.limiter.Take(session.ScopeUnauthenticated, clientAddressOf(ctx)); err != nil {
		return adminv1.ServerInfo{}, translateAdminError(err)
	}
	state, err := s.deps.claims.ClaimState(ctx)
	if err != nil {
		return adminv1.ServerInfo{}, fmt.Errorf("core: server info: %w", err)
	}
	linked := s.telegramLinked(ctx)
	return adminv1.ServerInfo{
		ServerID:       s.deps.serverID,
		APIVersion:     adminAPIVersion,
		ServerVersion:  s.deps.build.Version,
		State:          state,
		TelegramLinked: linked,
		PairingMethods: pairingMethods(state, linked),
		TLS:            s.deps.tls.info(),
		Time:           s.deps.clock.Now().UTC(),
	}, nil
}

func (s *setupEndpoints) telegramLinked(ctx context.Context) bool {
	snap, err := s.deps.relay.Status(ctx)
	if err != nil {
		s.deps.logger.WarnContext(ctx, "admin server info: telegram status unavailable", "component", "admin", "error", err)
		return false
	}
	return snap.State == identity.StateLinked
}

func pairingMethods(claimState string, linked bool) []string {
	if claimState != adminrepo.ClaimStateClaimed {
		return []string{}
	}
	if linked {
		return []string{pairingMethodHost, pairingMethodSaved}
	}
	return []string{pairingMethodHost}
}

func clientAddressOf(ctx context.Context) string {
	addr, ok := session.ClientAddrFromContext(ctx)
	if !ok {
		return ""
	}
	return addr
}

func translateAdminError(err error) error {
	var validation *setup.ValidationError
	var limited *session.RateLimitedError
	switch {
	case errors.As(err, &validation):
		return &handlers.ValidationError{Field: validation.Field, Reason: validation.Reason}
	case errors.As(err, &limited):
		return &handlers.RateLimitedError{Scope: string(limited.Scope), RetryAfter: limited.RetryAfter}
	case errors.Is(err, setup.ErrAlreadyClaimed), errors.Is(err, adminrepo.ErrAlreadyClaimed):
		return fmt.Errorf("%w: %w", handlers.ErrAlreadyClaimed, err)
	case errors.Is(err, setup.ErrSetupTokenInvalid), errors.Is(err, adminrepo.ErrSetupTokenInvalid):
		return fmt.Errorf("%w: %w", handlers.ErrSetupTokenInvalid, err)
	case errors.Is(err, session.ErrNotClaimed), errors.Is(err, adminrepo.ErrNotClaimed):
		return fmt.Errorf("%w: %w", handlers.ErrNotClaimed, err)
	case errors.Is(err, session.ErrPairingCodeInvalid), errors.Is(err, adminrepo.ErrPairingCodeInvalid):
		return fmt.Errorf("%w: %w", handlers.ErrPairingCodeInvalid, err)
	case errors.Is(err, session.ErrPairingUnavailable):
		return fmt.Errorf("%w: %w", handlers.ErrPairingUnavailable, err)
	case errors.Is(err, session.ErrUnsupportedDelivery):
		return &handlers.ValidationError{Field: "delivery", Reason: "must be saved_messages"}
	}
	return err
}

type limitedRelay struct {
	inner   setup.LoginRelay
	limiter *session.Limiter
	backend *BackendSlot
	links   LinkObserver
}

func (r *limitedRelay) linked(snap identity.Snapshot) identity.Snapshot {
	if r.links != nil && snap.State == identity.StateLinked {
		r.links.AccountLinked()
	}
	return snap
}

func (r *limitedRelay) Status(ctx context.Context) (identity.Snapshot, error) {
	return r.inner.Status(ctx)
}

func (r *limitedRelay) SaveCredentials(ctx context.Context, apiID int, apiHash string) error {
	if err := r.limiter.Take(session.ScopeTelegramApp, session.ServerWideKey); err != nil {
		return translateAdminError(err)
	}
	return r.inner.SaveCredentials(ctx, apiID, apiHash)
}

func (r *limitedRelay) DeleteCredentials(ctx context.Context) error {
	return r.inner.DeleteCredentials(ctx)
}

func (r *limitedRelay) Start(ctx context.Context, phone string) (identity.Snapshot, error) {
	if err := r.limiter.Take(session.ScopeTelegramLogin, session.ServerWideKey); err != nil {
		return identity.Snapshot{}, translateAdminError(err)
	}
	return r.inner.Start(ctx, phone)
}

func (r *limitedRelay) SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error) {
	snap, err := r.inner.SubmitCode(ctx, loginID, code)
	if err != nil {
		return snap, err
	}
	return r.linked(snap), nil
}

func (r *limitedRelay) SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error) {
	snap, err := r.inner.SubmitPassword(ctx, loginID, password)
	if err != nil {
		return snap, err
	}
	return r.linked(snap), nil
}

func (r *limitedRelay) Cancel(ctx context.Context) (identity.Snapshot, error) {
	return r.inner.Cancel(ctx)
}

func (r *limitedRelay) Unlink(ctx context.Context) (identity.Snapshot, error) {
	snap, err := r.inner.Unlink(ctx)
	if err != nil {
		return snap, err
	}
	r.backend.Clear()
	if r.links != nil {
		r.links.AccountUnlinked()
	}
	return snap, nil
}
