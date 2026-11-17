package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/tlscert"
	"github.com/PRIEYAN/ButterDB/internal/identity"
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
	Backend *BackendSlot

	session *TelegramSession
	keys    *keys.Service
	starter *StorageStarter
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
	if err := serving.assemble(ctx, a, files, tlsFiles); err != nil {
		_ = serving.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	serving.starter.Start(ctx)
	err = serving.starter.openAtBoot(ctx)
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

func (s *Serving) Runtime() *Runtime {
	if s.starter == nil {
		return nil
	}
	return s.starter.currentRuntime()
}

func (s *Serving) assemble(ctx context.Context, a *App, files blockcache.Files, tlsFiles tlscert.Filesystem) error {
	var err error
	if s.keys, err = NewKeyService(s.session.control, s.session.ring, a.clock, cryptorand.Reader); err != nil {
		return err
	}
	if s.starter, err = NewStorageStarter(StorageStarterDependencies{
		Config: a.cfg, Data: a.DataDir(), FS: a.fsys, Files: files, Control: s.session.control, Ring: s.session.ring, Keys: s.keys,
		Accounts: s.session.accounts, Source: s.session, Projections: appProjections{app: a}, Slot: s.Backend, Clock: a.clock,
		Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: a.logger, Embedded: a.embed,
	}); err != nil {
		return err
	}
	return s.startAdmin(ctx, a, tlsFiles)
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
	s.Admin, err = NewAdmin(AdminDependencies{
		Config: a.cfg, Data: a.DataDir(), Control: s.session.control, Keys: s.keys, Relay: s.session.Identity(),
		Messages: telegramSavedMessages{telegram: s.session.Telegram}, Backend: s.Backend, ServerID: serverID,
		Build: a.build, Files: files, Clock: a.clock, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: a.logger,
		LinkEvents: s.starter, Registry: s.starter, Embedded: a.embed != nil,
	})
	if err != nil {
		return err
	}
	if a.embed != nil {
		return nil
	}
	return s.Admin.Start(ctx)
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
	var adminErr, starterErr error
	if s.Admin != nil {
		adminErr = s.Admin.Close(ctx)
	}
	if s.starter != nil {
		starterErr = s.starter.Close(ctx)
	}
	s.Backend.Clear()
	sessionErr := s.session.Close(ctx)
	s.session = nil
	return errors.Join(adminErr, starterErr, sessionErr)
}

type appProjections struct {
	app *App
}

func (p appProjections) OpenProjectionDatabase(ctx context.Context) (*sqlitedb.DB, error) {
	return p.app.openProjectionDatabase(ctx)
}
