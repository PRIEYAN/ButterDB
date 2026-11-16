package session

import (
	"context"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

type ClaimStateSource interface {
	ClaimState(ctx context.Context) (string, error)
}

type SetupTokenMinter interface {
	MintSetupTokenIfUnclaimed(ctx context.Context) (string, time.Time, error)
}

type HostCodeIssuer interface {
	HostCode(ctx context.Context) (string, time.Time, error)
}

type AdminTokenDependencies struct {
	Claims  ClaimStateSource
	Setup   SetupTokenMinter
	Pairing HostCodeIssuer
	Audit   AuditWriter
	Clock   clock.Clock
}

type AdminTokenHandler struct {
	deps AdminTokenDependencies
}

func NewAdminTokenHandler(deps AdminTokenDependencies) (*AdminTokenHandler, error) {
	switch {
	case deps.Claims == nil:
		return nil, fmt.Errorf("session: %w: claim state", errs.ErrMissingDependency)
	case deps.Setup == nil:
		return nil, fmt.Errorf("session: %w: setup token minter", errs.ErrMissingDependency)
	case deps.Pairing == nil:
		return nil, fmt.Errorf("session: %w: host code issuer", errs.ErrMissingDependency)
	case deps.Audit == nil:
		return nil, fmt.Errorf("session: %w: audit writer", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("session: %w: clock", errs.ErrMissingDependency)
	}
	return &AdminTokenHandler{deps: deps}, nil
}

func (h *AdminTokenHandler) AdminToken(ctx context.Context) (string, string, time.Time, error) {
	state, err := h.deps.Claims.ClaimState(ctx)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("session: claim state: %w", err)
	}
	kind, action := KindPairingCode, "pairing.code_issued"
	issue := h.deps.Pairing.HostCode
	if state == adminrepo.ClaimStateUnclaimed {
		kind, action = KindSetupToken, "setup.token_issued"
		issue = h.deps.Setup.MintSetupTokenIfUnclaimed
	}
	value, expiresAt, err := issue(ctx)
	if auditErr := h.audit(ctx, action, err); auditErr != nil {
		return "", "", time.Time{}, auditErr
	}
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("session: issue %s: %w", kind, err)
	}
	return kind, value, expiresAt, nil
}

func (h *AdminTokenHandler) audit(ctx context.Context, action string, issueErr error) error {
	entry := adminrepo.AuditEntry{
		At:        h.deps.Clock.Now(),
		ActorType: "host",
		Action:    action,
		Outcome:   adminrepo.AuditOutcomeSuccess,
	}
	if issueErr != nil {
		entry.Outcome = adminrepo.AuditOutcomeFailure
		entry.ErrorCode = "internal"
	}
	if _, err := h.deps.Audit.InsertAudit(ctx, entry); err != nil {
		return fmt.Errorf("session: audit admin token: %w", err)
	}
	return nil
}
