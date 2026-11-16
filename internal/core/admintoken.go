package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var (
	errOfflineRelay = errors.New("core: telegram relay is not available in this command")

	_ setup.LoginRelay            = offlineRelay{}
	_ session.SavedMessagesSender = offlineDelivery{}
)

type offlineRelay struct{}

func (offlineRelay) Status(context.Context) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

func (offlineRelay) SaveCredentials(context.Context, int, string) error {
	return errOfflineRelay
}

func (offlineRelay) DeleteCredentials(context.Context) error {
	return errOfflineRelay
}

func (offlineRelay) Start(context.Context, string) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

func (offlineRelay) SubmitCode(context.Context, string, string) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

func (offlineRelay) SubmitPassword(context.Context, string, string) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

func (offlineRelay) Cancel(context.Context) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

func (offlineRelay) Unlink(context.Context) (identity.Snapshot, error) {
	return identity.Snapshot{}, errOfflineRelay
}

type offlineDelivery struct{}

func (offlineDelivery) SendToSavedMessages(context.Context, string) error {
	return fmt.Errorf("core: %w: no telegram connection in this command", session.ErrPairingUnavailable)
}

type AdminTokenResult struct {
	Kind      string
	Value     string
	ExpiresAt time.Time
}

func (a *App) IssueAdminToken(ctx context.Context) (AdminTokenResult, error) {
	dir := a.DataDir()
	if dir == nil {
		return AdminTokenResult{}, ErrAppNotStarted
	}
	control, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, dir.Join(controlDatabaseFile), sqlitedb.Options{}, a.logger,
		sqlitedb.Dependencies{Clock: a.clock, FS: a.fsys})
	if err != nil {
		return AdminTokenResult{}, fmt.Errorf("core: open control database: %w", err)
	}
	defer func() { _ = control.Close() }()
	if _, err := schema.Apply(ctx, control, schema.Control); err != nil {
		return AdminTokenResult{}, fmt.Errorf("core: migrate control database: %w", err)
	}
	handler, err := a.offlineTokenHandler(ctx, control)
	if err != nil {
		return AdminTokenResult{}, err
	}
	kind, value, expiresAt, err := handler.AdminToken(ctx)
	if err != nil {
		return AdminTokenResult{}, fmt.Errorf("core: admin token: %w", err)
	}
	return AdminTokenResult{Kind: kind, Value: value, ExpiresAt: expiresAt}, nil
}

func (a *App) offlineTokenHandler(ctx context.Context, control *sqlitedb.DB) (*session.AdminTokenHandler, error) {
	meta, err := newAdminMeta(control, cryptorand.Reader)
	if err != nil {
		return nil, err
	}
	serverID, err := meta.EnsureDeploymentID(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := adminrepo.New(control, a.clock)
	if err != nil {
		return nil, fmt.Errorf("core: admin repository: %w", err)
	}
	quiet := slog.New(slog.DiscardHandler)
	issuer, err := setup.New(setup.Dependencies{
		Relay: offlineRelay{}, Claims: claimStore{repo: repo}, Tokens: session.NewTokens(cryptorand.Reader), Clock: a.clock,
		Random: cryptorand.Reader, Logger: quiet, TTL: a.cfg.Admin.SetupTokenTTL.Std(), ServerID: serverID,
	})
	if err != nil {
		return nil, fmt.Errorf("core: setup service: %w", err)
	}
	pairing := session.NewPairing(repo, offlineDelivery{}, session.NewTokens(cryptorand.Reader), a.clock, cryptorand.Reader)
	handler, err := session.NewAdminTokenHandler(session.AdminTokenDependencies{
		Claims: repo, Setup: issuer, Pairing: pairing, Audit: repo, Clock: a.clock,
	})
	if err != nil {
		return nil, fmt.Errorf("core: admin token handler: %w", err)
	}
	return handler, nil
}
