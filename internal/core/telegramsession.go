package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const controlDatabaseFile = "control.db"

var ErrAppNotStarted = errors.New("core: app not started")

type TelegramSession struct {
	*Telegram
	app      *App
	ring     *keyring.Ring
	control  *sqlitedb.DB
	accounts *accountrepo.Repository
}

func (a *App) OpenTelegram(ctx context.Context, role identity.Role) (*TelegramSession, error) {
	dir := a.DataDir()
	if dir == nil {
		return nil, ErrAppNotStarted
	}
	ring, err := keyring.Load(a.cfg.ServerKeyFile)
	if err != nil {
		return nil, fmt.Errorf("core: server key: %w", err)
	}
	control, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, dir.Join(controlDatabaseFile), sqlitedb.Options{}, a.logger,
		sqlitedb.Dependencies{Clock: a.clock, FS: a.fsys})
	if err != nil {
		ring.Close()
		return nil, fmt.Errorf("core: open control database: %w", err)
	}
	session, err := a.assembleSession(ctx, role, ring, control)
	if err != nil {
		_ = control.Close()
		ring.Close()
		return nil, err
	}
	return session, nil
}

func (a *App) assembleSession(ctx context.Context, role identity.Role, ring *keyring.Ring, control *sqlitedb.DB) (*TelegramSession, error) {
	if _, err := schema.Apply(ctx, control, schema.Control); err != nil {
		return nil, fmt.Errorf("core: migrate control database: %w", err)
	}
	accounts, err := accountrepo.New(control)
	if err != nil {
		return nil, fmt.Errorf("core: account repository: %w", err)
	}
	telegram, err := NewTelegram(TelegramDependencies{
		Config:   a.cfg,
		Role:     role,
		Clock:    a.clock,
		FS:       a.fsys,
		Random:   cryptorand.Reader,
		Sleeper:  flow.SystemSleeper{},
		Logger:   a.logger,
		Keys:     ring,
		Accounts: accounts,
	})
	if err != nil {
		return nil, err
	}
	return &TelegramSession{Telegram: telegram, app: a, ring: ring, control: control, accounts: accounts}, nil
}

func (s *TelegramSession) Close(ctx context.Context) error {
	closeErr := s.Telegram.Close(ctx)
	dbErr := s.control.Close()
	s.ring.Close()
	return errors.Join(closeErr, dbErr)
}
