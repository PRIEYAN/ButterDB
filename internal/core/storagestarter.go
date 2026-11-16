package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/readpath/blockcache"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer/livesync"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	RegistryAbsent      = "absent"
	RegistryPending     = "pending"
	RegistryVerified    = "verified"
	RegistryRediscovery = "rediscovery"
	RegistryRefused     = "refused"

	storageRetryFirst   = 2 * time.Second
	storageRetryCeiling = time.Minute
)

var errAdoptionNeeded = errors.New("core: a registry in this account needs the owner to adopt it")

type RegistryReport struct {
	Status string
	Detail string
}

type LinkObserver interface {
	AccountLinked()
	AccountUnlinked()
}

type RegistryReporter interface {
	RegistryReport() RegistryReport
}

type PrimaryAccountRecords interface {
	Get(ctx context.Context, role string) (accountrepo.Record, bool, error)
}

type StorageTransport struct {
	Channels tgclient.ChannelRPC
	Sender   tgclient.Sender
	Prepaced tgclient.Sender
	Reader   tgclient.RangeReader
	Signals  livesync.SignalSource
	Pacer    admission.Pacer
}

type StorageSource interface {
	StorageTransport(ctx context.Context) (StorageTransport, error)
}

type ProjectionOpener interface {
	OpenProjectionDatabase(ctx context.Context) (*sqlitedb.DB, error)
}

type StorageStarterDependencies struct {
	Config      config.Config
	Data        *datadir.Dir
	FS          fsx.FS
	Files       blockcache.Files
	Control     *sqlitedb.DB
	Ring        *keyring.Ring
	Keys        *keys.Service
	Accounts    PrimaryAccountRecords
	Source      StorageSource
	Projections ProjectionOpener
	Slot        *BackendSlot
	Clock       clock.Clock
	Sleeper     flow.Sleeper
	Random      io.Reader
	Logger      *slog.Logger
}

type reconcileOutcome int

const (
	reconcileSettled reconcileOutcome = iota
	reconcileAgain
	reconcileRetry
)

type StorageStarter struct {
	deps StorageStarterDependencies
	wake chan struct{}

	openMu sync.Mutex

	mu            sync.Mutex
	runtime       *Runtime
	database      *sqlitedb.DB
	report        RegistryReport
	attemptCancel context.CancelFunc
	pauseCancel   context.CancelFunc
	cancel        context.CancelFunc
	done          chan struct{}
}

func NewStorageStarter(deps StorageStarterDependencies) (*StorageStarter, error) {
	if err := validateStorageStarter(deps); err != nil {
		return nil, err
	}
	return &StorageStarter{deps: deps, wake: make(chan struct{}, 1), report: RegistryReport{Status: RegistryAbsent}}, nil
}

func validateStorageStarter(deps StorageStarterDependencies) error {
	missing := ""
	switch {
	case deps.Data == nil:
		missing = "data directory"
	case deps.FS == nil:
		missing = "filesystem"
	case deps.Files == nil:
		missing = "cache files"
	case deps.Control == nil:
		missing = "control database"
	case deps.Ring == nil:
		missing = "server key"
	case deps.Keys == nil:
		missing = "key service"
	case deps.Accounts == nil:
		missing = "accounts"
	case deps.Source == nil:
		missing = "telegram source"
	case deps.Projections == nil:
		missing = "projection opener"
	case deps.Slot == nil:
		missing = "backend slot"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Sleeper == nil:
		missing = "sleeper"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return fmt.Errorf("core: storage starter: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}

func (s *StorageStarter) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(runCtx, s.done)
}

func (s *StorageStarter) Close(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("core: storage starter: stop: %w", ctx.Err())
		}
	}
	s.openMu.Lock()
	defer s.openMu.Unlock()
	return s.closeRuntime(ctx)
}

func (s *StorageStarter) AccountLinked() {
	s.signal()
}

func (s *StorageStarter) AccountUnlinked() {
	s.mu.Lock()
	if s.attemptCancel != nil {
		s.attemptCancel()
	}
	s.mu.Unlock()
	s.signal()
}

func (s *StorageStarter) RegistryReport() RegistryReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report
}

func (s *StorageStarter) currentRuntime() *Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime
}

func (s *StorageStarter) signal() {
	s.mu.Lock()
	if s.pauseCancel != nil {
		s.pauseCancel()
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *StorageStarter) setReport(status, detail string) {
	s.mu.Lock()
	s.report = RegistryReport{Status: status, Detail: detail}
	s.mu.Unlock()
}

func (s *StorageStarter) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		s.reconcile(ctx)
	}
}

func (s *StorageStarter) reconcile(ctx context.Context) {
	delay := storageRetryFirst
	for ctx.Err() == nil {
		switch s.reconcileOnce(ctx) {
		case reconcileSettled:
			return
		case reconcileAgain:
			delay = storageRetryFirst
		case reconcileRetry:
			if s.pause(ctx, delay) {
				delay = storageRetryFirst
				continue
			}
			delay = min(delay*2, storageRetryCeiling)
		}
	}
}

func (s *StorageStarter) pause(ctx context.Context, delay time.Duration) bool {
	pauseCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.pauseCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pauseCancel = nil
		s.mu.Unlock()
		cancel()
	}()
	if err := s.deps.Sleeper.Sleep(pauseCtx, delay); err == nil {
		return false
	}
	return ctx.Err() == nil
}

func (s *StorageStarter) reconcileOnce(ctx context.Context) reconcileOutcome {
	record, found, err := s.deps.Accounts.Get(ctx, primaryRole)
	if err != nil {
		s.deps.Logger.Error("storage start failed: read primary account", "component", "lifecycle", "error", err)
		return reconcileRetry
	}
	if !found || !record.Linked {
		s.stopAfterUnlink(ctx)
		return reconcileSettled
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.attemptCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.attemptCancel = nil
		s.mu.Unlock()
		cancel()
	}()
	return s.judge(ctx, attemptCtx, s.bringUp(attemptCtx))
}

func (s *StorageStarter) judge(ctx, attemptCtx context.Context, err error) reconcileOutcome {
	var startup *StartupError
	switch {
	case err == nil:
		return reconcileSettled
	case ctx.Err() != nil:
		return reconcileSettled
	case attemptCtx.Err() != nil:
		return reconcileAgain
	case errors.Is(err, errAdoptionNeeded), errors.As(err, &startup) && startup.Reason == ReasonForeignRegistry:
		s.reportRediscovery(err)
		return reconcileSettled
	case errors.As(err, &startup):
		s.setReport(RegistryRefused, string(startup.Reason))
		s.deps.Logger.Error("storage start refused", "component", "lifecycle", "reason", string(startup.Reason))
		return reconcileSettled
	}
	s.deps.Logger.Error("storage start failed", "component", "lifecycle", "error", err)
	return reconcileRetry
}

func (s *StorageStarter) reportRediscovery(err error) {
	detail := string(ReasonRegistryUnknown)
	var startup *StartupError
	if errors.As(err, &startup) {
		detail = string(startup.Reason)
	}
	s.setReport(RegistryRediscovery, detail)
	s.deps.Logger.Warn("registry channels exist in this telegram account and no state file; adopt one with: butterdbd setup --registry-channel <id>",
		"component", "lifecycle")
}

func (s *StorageStarter) bringUp(ctx context.Context) error {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if s.currentRuntime() != nil {
		return nil
	}
	if !fileExists(s.deps.Data.Join(stateFileName)) {
		s.setReport(RegistryAbsent, "")
		if err := s.ensureRegistry(ctx); err != nil {
			return err
		}
	}
	return s.openLocked(ctx)
}

func (s *StorageStarter) openAtBoot(ctx context.Context) error {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if s.currentRuntime() != nil {
		return nil
	}
	return s.openLocked(ctx)
}

func (s *StorageStarter) openLocked(ctx context.Context) error {
	s.setReport(RegistryPending, "")
	if err := s.open(ctx); err != nil {
		if errors.Is(err, ErrNotLinked) {
			s.setReport(RegistryAbsent, "")
		}
		return err
	}
	s.setReport(RegistryVerified, "")
	return nil
}

func (s *StorageStarter) ensureRegistry(ctx context.Context) error {
	record, found, err := s.deps.Accounts.Get(ctx, primaryRole)
	if err != nil {
		return fmt.Errorf("core: read primary account: %w", err)
	}
	if !found || !record.Linked {
		return ErrNoPrimaryAccount
	}
	transport, err := s.deps.Source.StorageTransport(ctx)
	if err != nil {
		return err
	}
	writer, err := secretfile.New(s.deps.FS)
	if err != nil {
		return fmt.Errorf("core: registry setup: secret writer: %w", err)
	}
	database, err := s.deps.Projections.OpenProjectionDatabase(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	projection, err := NewProjection(ProjectionDependencies{Logger: s.deps.Logger})
	if err != nil {
		return err
	}
	_, err = SetupRegistry(ctx, RegistrySetupDependencies{
		Database: database, Control: s.deps.Control, Projection: projection, Telegram: transport.Channels, Sender: transport.Sender,
		Signals: transport.Signals, Ring: s.deps.Ring, PrimaryUserID: record.UserID, StatePath: s.deps.Data.Join(stateFileName),
		Writer: writer, Terminal: adminTerminal{}, Clock: s.deps.Clock, Sleeper: s.deps.Sleeper, Random: s.deps.Random,
		Logger: s.deps.Logger,
	}, RegistryOptions{})
	return err
}

func (s *StorageStarter) open(ctx context.Context) error {
	if !fileExists(s.deps.Data.Join(stateFileName)) {
		return ErrNotLinked
	}
	record, found, err := s.deps.Accounts.Get(ctx, primaryRole)
	if err != nil {
		return fmt.Errorf("core: read primary account: %w", err)
	}
	if !found || !record.Linked {
		return ErrNotLinked
	}
	writer, err := secretfile.New(s.deps.FS)
	if err != nil {
		return fmt.Errorf("core: serving: secret writer: %w", err)
	}
	state, err := NewRegistryStateStore(s.deps.Data.Join(stateFileName), writer, s.deps.Control, s.deps.Random)
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
	database, err := s.deps.Projections.OpenProjectionDatabase(ctx)
	if err != nil {
		return err
	}
	runtime, err := s.startRuntime(ctx, database, state, record.UserID, registry.DeploymentID)
	if err != nil {
		_ = database.Close()
		return err
	}
	s.mu.Lock()
	s.runtime, s.database = runtime, database
	s.mu.Unlock()
	s.deps.Slot.Publish(runtime.Backend())
	return nil
}

func (s *StorageStarter) startRuntime(ctx context.Context, database *sqlitedb.DB, state buckets.RegistryStateStore, userID int64, deploymentID string) (*Runtime, error) {
	transport, err := s.deps.Source.StorageTransport(ctx)
	if err != nil {
		return nil, err
	}
	runtime, err := NewRuntime(RuntimeDependencies{
		Config: s.deps.Config, Data: s.deps.Data, Control: s.deps.Control, Database: database, Ring: s.deps.Ring,
		State: state, Telegram: transport.Channels, Sender: transport.Sender, Prepaced: transport.Prepaced,
		Origin: transport.Reader, Signals: transport.Signals, Pacer: transport.Pacer,
		ActorUserID: userID, DeploymentID: deploymentID, FS: s.deps.FS, Files: s.deps.Files,
		Clock: s.deps.Clock, Sleeper: s.deps.Sleeper, Random: s.deps.Random, Logger: s.deps.Logger, Keys: s.deps.Keys,
	})
	if err != nil {
		return nil, err
	}
	if err := runtime.Start(ctx); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (s *StorageStarter) stopAfterUnlink(ctx context.Context) {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.deps.Config.Shutdown.TotalTimeout.Std())
	defer cancel()
	if err := s.closeRuntime(closeCtx); err != nil {
		s.deps.Logger.Error("storage stop after unlink failed", "component", "lifecycle", "error", err)
	}
	s.setReport(RegistryAbsent, "")
}

func (s *StorageStarter) closeRuntime(ctx context.Context) error {
	s.deps.Slot.Clear()
	s.mu.Lock()
	runtime, database := s.runtime, s.database
	s.runtime, s.database = nil, nil
	s.mu.Unlock()
	var runtimeErr, databaseErr error
	if runtime != nil {
		runtimeErr = runtime.Close(ctx)
	}
	if database != nil {
		databaseErr = database.Close()
	}
	return errors.Join(runtimeErr, databaseErr)
}

type adminTerminal struct{}

func (adminTerminal) Say(string) {}

func (adminTerminal) Prompt(label string) (string, error) {
	if label == createRegistryPrompt {
		return confirmCreateAnswer, nil
	}
	return "", errAdoptionNeeded
}

func (adminTerminal) PromptSecret(string) (string, error) {
	return "", errAdoptionNeeded
}
