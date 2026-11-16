package core

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func (s *TelegramSession) EnsureRegistry(ctx context.Context, terminal Terminal, opts RegistryOptions) (RegistryOutcome, error) {
	record, found, err := s.accounts.Get(ctx, primaryRole)
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: read primary account: %w", err)
	}
	if !found || !record.Linked {
		return RegistryOutcome{}, ErrNoPrimaryAccount
	}
	transport, err := s.Transport(ctx)
	if err != nil {
		return RegistryOutcome{}, err
	}
	writer, err := secretfile.New(s.app.fsys)
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: secret writer: %w", err)
	}
	projectionDB, err := s.app.openProjectionDatabase(ctx)
	if err != nil {
		return RegistryOutcome{}, err
	}
	defer func() { _ = projectionDB.Close() }()
	projection, err := NewProjection(ProjectionDependencies{Logger: s.app.logger})
	if err != nil {
		return RegistryOutcome{}, err
	}
	return SetupRegistry(ctx, RegistrySetupDependencies{
		Database:      projectionDB,
		Control:       s.control,
		Projection:    projection,
		Telegram:      transport.Channels,
		Sender:        transport.Sender,
		Signals:       transport.Activity,
		Ring:          s.ring,
		PrimaryUserID: record.UserID,
		StatePath:     s.app.DataDir().Join(stateFileName),
		Writer:        writer,
		Terminal:      terminal,
		Clock:         s.app.clock,
		Sleeper:       flow.SystemSleeper{},
		Random:        cryptorand.Reader,
		Logger:        s.app.logger,
	}, opts)
}

func (s *TelegramSession) StorageTransport(ctx context.Context) (StorageTransport, error) {
	transport, err := s.Transport(ctx)
	if err != nil {
		return StorageTransport{}, err
	}
	return StorageTransport{
		Channels: transport.Channels, Sender: transport.Sender, Prepaced: transport.Prepaced, Reader: transport.Reader,
		Signals: transport.Activity, Pacer: transport.Flow.Limiter,
	}, nil
}
