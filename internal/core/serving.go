package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/tlscert"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/readpath/blockcache"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var (
	ErrNotLinked      = errors.New("core: deployment is not linked")
	errFilesUnusable  = errors.New("core: filesystem cannot serve the block cache")
	errTLSUnusable    = errors.New("core: filesystem cannot serve the admin tls files")
	errServingStopped = errors.New("core: serving already stopped")
)

type Serving struct {
	Admin   *Admin
	Runtime *Runtime
	Backend *BackendSlot

	session      *TelegramSession
	keys         *keys.Service
	projectionDB *sqlitedb.DB
}

func (a *App) StartServing(ctx context.Context) (*Serving, error) {
	dir := a.DataDir()
	if dir == nil {
		return nil, ErrAppNotStarted
	}
	files, ok := a.fsys.(blockcache.Files)
	if !ok {
		return nil, errFilesUnusable
	}
	tlsFiles, ok := a.fsys.(tlscert.Filesystem)
	if !ok {
		return nil, errTLSUnusable
	}
	if err := a.ensureServerKey(); err != nil {
		return nil, err
	}
	session, err := a.OpenTelegram(ctx, identity.RolePrimary)
	if err != nil {
		return nil, err
	}
	serving := &Serving{session: session, Backend: &BackendSlot{}}
	if err := serving.startAdmin(ctx, a, tlsFiles); err != nil {
		_ = serving.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	err = serving.open(ctx, a, files)
	if errors.Is(err, ErrNotLinked) {
		a.logger.Info("s3 listener not started: deployment is not linked", "component", "lifecycle")
		return serving, nil
	}
	if err != nil {
		_ = serving.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return serving, nil
}

func (s *Serving) startAdmin(ctx context.Context, a *App, files tlscert.Filesystem) error {
	meta, err := newAdminMeta(s.session.control, cryptorand.Reader)
	if err != nil {
		return err
	}
	serverID, err := meta.EnsureDeploymentID(ctx)
	if err != nil {
		return err
	}
	if s.keys, err = NewKeyService(s.session.control, s.session.ring, a.clock, cryptorand.Reader); err != nil {
		return err
	}
	s.Admin, err = NewAdmin(AdminDependencies{
		Config: a.cfg, Data: a.DataDir(), Control: s.session.control, Keys: s.keys, Relay: s.session.Identity(),
		Messages: telegramSavedMessages{telegram: s.session.Telegram}, Backend: s.Backend, ServerID: serverID,
		Build: a.build, Files: files, Clock: a.clock, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: a.logger,
	})
	if err != nil {
		return err
	}
	return s.Admin.Start(ctx)
}

func (s *Serving) open(ctx context.Context, a *App, files blockcache.Files) error {
	if !fileExists(a.DataDir().Join(stateFileName)) {
		return ErrNotLinked
	}
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
		Clock: a.clock, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: a.logger, Keys: s.keys,
	})
	if err != nil {
		return err
	}
	if err := s.Runtime.Start(ctx); err != nil {
		return err
	}
	s.Backend.Publish(s.Runtime.Backend())
	return nil
}

func (s *Serving) ReloadCertificate() error {
	if s.Admin == nil {
		return nil
	}
	return s.Admin.ReloadCertificate()
}

func (s *Serving) Close(ctx context.Context) error {
	if s.session == nil {
		return errServingStopped
	}
	var adminErr, runtimeErr, dbErr error
	if s.Admin != nil {
		adminErr = s.Admin.Close(ctx)
	}
	s.Backend.Clear()
	if s.Runtime != nil {
		runtimeErr = s.Runtime.Close(ctx)
	}
	if s.projectionDB != nil {
		dbErr = s.projectionDB.Close()
	}
	sessionErr := s.session.Close(ctx)
	s.session = nil
	return errors.Join(adminErr, runtimeErr, dbErr, sessionErr)
}
