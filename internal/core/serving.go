package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/readpath/blockcache"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var (
	ErrNotLinked      = errors.New("core: deployment is not linked")
	errFilesUnusable  = errors.New("core: filesystem cannot serve the block cache")
	errServingStopped = errors.New("core: serving already stopped")
)

type Serving struct {
	Runtime *Runtime

	session      *TelegramSession
	projectionDB *sqlitedb.DB
}

func (a *App) StartServing(ctx context.Context) (*Serving, error) {
	files, ok := a.fsys.(blockcache.Files)
	if !ok {
		return nil, errFilesUnusable
	}
	session, err := a.OpenTelegram(ctx, identity.RolePrimary)
	if err != nil {
		return nil, err
	}
	serving := &Serving{session: session}
	if err := serving.open(ctx, a, files); err != nil {
		_ = serving.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return serving, nil
}

func (s *Serving) open(ctx context.Context, a *App, files blockcache.Files) error {
	record, found, err := s.session.accounts.Get(ctx, primaryRole)
	if err != nil {
		return fmt.Errorf("core: read primary account: %w", err)
	}
	if !found || !record.Linked {
		return ErrNotLinked
	}
	writer, err := secretfile.New(a.fsys)
	if err != nil {
		return fmt.Errorf("core: serving: secret writer: %w", err)
	}
	state, err := NewRegistryStateStore(a.DataDir().Join(stateFileName), writer, s.session.control, cryptorand.Reader)
	if err != nil {
		return err
	}
	registry, found, err := state.LoadRegistryState(ctx)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotLinked
	}
	if s.projectionDB, err = a.openProjectionDatabase(ctx); err != nil {
		return err
	}
	transport, err := s.session.Transport(ctx)
	if err != nil {
		return err
	}
	s.Runtime, err = NewRuntime(RuntimeDependencies{
		Config: a.cfg, Data: a.DataDir(), Control: s.session.control, Database: s.projectionDB, Ring: s.session.ring,
		State: state, Telegram: transport.Channels, Sender: transport.Sender, Prepaced: transport.Prepaced,
		Origin: transport.Reader, Signals: transport.Activity, Pacer: transport.Flow.Limiter,
		ActorUserID: record.UserID, DeploymentID: registry.DeploymentID, FS: a.fsys, Files: files,
		Clock: a.clock, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: a.logger,
	})
	if err != nil {
		return err
	}
	return s.Runtime.Start(ctx)
}

func (s *Serving) Close(ctx context.Context) error {
	if s.session == nil {
		return errServingStopped
	}
	var runtimeErr, dbErr error
	if s.Runtime != nil {
		runtimeErr = s.Runtime.Close(ctx)
	}
	if s.projectionDB != nil {
		dbErr = s.projectionDB.Close()
	}
	sessionErr := s.session.Close(ctx)
	s.session = nil
	return errors.Join(runtimeErr, dbErr, sessionErr)
}
