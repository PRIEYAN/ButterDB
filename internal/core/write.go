package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/ingest/remove"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/ingest/sweeper"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

const (
	spoolDirName         = "spool"
	multipartDirName     = "multipart"
	defaultRequestBudget = 30 * time.Second
)

var _ lifecycle.Component = (*Write)(nil)

type WriteDependencies struct {
	Config      config.Config
	Data        *datadir.Dir
	Control     *sqlitedb.DB
	Database    *sqlitedb.DB
	Projection  *Projection
	Sync        *Sync
	Telegram    tgclient.ChannelRPC
	Sender      tgclient.Sender
	Prepaced    tgclient.Sender
	Pacer       admission.Pacer
	Ring        *keyring.Ring
	ActorUserID int64
	FS          fsx.FS
	Clock       clock.Clock
	Sleeper     flow.Sleeper
	Random      io.Reader
	Logger      *slog.Logger
	Sweeper     sweeper.Config
}

type Write struct {
	Spool     *spool.Spool
	Admission *admission.Controller
	Jobs      *jobrepo.Repository
	Multipart *jobrepo.MultipartIndex
	Cleanup   *objectrepo.CleanupQueue
	Drainer   *remove.Drainer
	KeyLocks  *put.ObjectKeyLocks
	Put       *put.Putter
	Remove    *remove.Deleter
	Stage     *multipartstage.Stage
	Sweeper   *sweeper.Sweeper
}

func NewWrite(deps WriteDependencies) (*Write, error) {
	if err := validateWriteDependencies(deps); err != nil {
		return nil, err
	}
	w := &Write{}
	if err := w.assembleStorage(deps); err != nil {
		return nil, err
	}
	if err := w.assembleServices(deps); err != nil {
		return nil, err
	}
	if err := w.assembleBackground(deps); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Write) assembleStorage(deps WriteDependencies) error {
	var err error
	if w.Jobs, err = jobrepo.New(deps.Control, deps.Clock); err != nil {
		return fmt.Errorf("core: write: job repository: %w", err)
	}
	if w.Multipart, err = jobrepo.NewMultipartIndex(deps.Control); err != nil {
		return fmt.Errorf("core: write: multipart index: %w", err)
	}
	if w.Cleanup, err = objectrepo.NewCleanupQueue(deps.Database); err != nil {
		return fmt.Errorf("core: write: cleanup queue: %w", err)
	}
	spoolDir, err := deps.Data.Subdir(spoolDirName)
	if err != nil {
		return fmt.Errorf("core: write: spool directory: %w", err)
	}
	budget := spool.Budget{MaxBytes: int64(deps.Config.Spool.MaxBytes), MaxConcurrent: deps.Config.Ingest.MaxConcurrentPuts}
	if w.Spool, err = spool.NewWithBudget(spoolDir, deps.FS, deps.Clock, budget); err != nil {
		return fmt.Errorf("core: write: spool: %w", err)
	}
	w.Admission, err = admission.New(admission.Dependencies{
		Config: admission.Config{
			MaxConcurrentPuts:       deps.Config.Ingest.MaxConcurrentPuts,
			MaxConcurrentPutsPerKey: deps.Config.Ingest.MaxConcurrentPutsPerKey,
		},
		Pacer:   deps.Pacer,
		Clock:   deps.Clock,
		Sleeper: deps.Sleeper,
	})
	if err != nil {
		return fmt.Errorf("core: write: admission: %w", err)
	}
	return nil
}

func (w *Write) assembleServices(deps WriteDependencies) error {
	bucketRepo, err := bucketrepo.New(deps.Database)
	if err != nil {
		return fmt.Errorf("core: write: bucket repository: %w", err)
	}
	objects, err := objectrepo.New(deps.Database)
	if err != nil {
		return fmt.Errorf("core: write: object repository: %w", err)
	}
	peers := &writePeers{peers: &channelPeers{channels: deps.Sync.Channels, resolver: deps.Telegram}}
	w.Drainer, err = remove.NewDrainer(remove.DrainerDependencies{
		Queue: w.Cleanup, Peers: peers, Messages: deps.Telegram, Clock: deps.Clock, Logger: deps.Logger,
	})
	if err != nil {
		return fmt.Errorf("core: write: cleanup drainer: %w", err)
	}
	w.KeyLocks = put.NewKeyLocks()
	budget := deps.Config.S3.RequestBudget.Std()
	if budget <= 0 {
		budget = defaultRequestBudget
	}
	w.Put, err = put.New(put.Dependencies{
		Spool:          w.Spool,
		Admission:      w.Admission,
		Jobs:           &uploadJobs{repo: w.Jobs},
		Buckets:        bucketRepo,
		Authority:      deps.Sync.Buckets,
		Gates:          deps.Sync.WriteGates,
		Objects:        objects,
		Keys:           &encryptionKeys{repo: bucketRepo},
		Unwrap:         &dekUnwrapper{ring: deps.Ring},
		Peers:          peers,
		KeyLocks:       w.KeyLocks,
		Bodies:         deps.Prepaced,
		Controls:       deps.Prepaced,
		Deleter:        deps.Telegram,
		Messages:       deps.Telegram,
		Projector:      deps.Projection.Projector,
		Writer:         deps.Database,
		Cleanup:        w.Drainer,
		StableRandomID: send.StableRandomID,
		Clock:          deps.Clock,
		Random:         deps.Random,
		Logger:         deps.Logger,
		Config: put.Config{
			RequestBudget:  budget,
			KEKFingerprint: deps.Ring.FingerprintString(),
			ActorUserID:    deps.ActorUserID,
		},
	})
	if err != nil {
		return fmt.Errorf("core: write: put service: %w", err)
	}
	w.Remove, err = remove.New(remove.Dependencies{
		Buckets:        bucketRepo,
		Gates:          deps.Sync.WriteGates,
		Authority:      deps.Sync.Buckets,
		KeyLocks:       w.KeyLocks,
		Objects:        objects,
		Operations:     &operationLookup{db: deps.Database},
		Jobs:           &deleteJobs{repo: w.Jobs},
		Sender:         deps.Sender,
		Peers:          peers,
		Messages:       deps.Telegram,
		Projector:      deps.Projection.Projector,
		Writer:         deps.Database,
		Drainer:        w.Drainer,
		Principals:     principalContext{},
		StableRandomID: send.StableRandomID,
		Clock:          deps.Clock,
		Random:         deps.Random,
		Logger:         deps.Logger,
		ActorUserID:    deps.ActorUserID,
	})
	if err != nil {
		return fmt.Errorf("core: write: remove service: %w", err)
	}
	return nil
}

func (w *Write) assembleBackground(deps WriteDependencies) error {
	multipartDir, err := deps.Data.Subdir(multipartDirName)
	if err != nil {
		return fmt.Errorf("core: write: multipart directory: %w", err)
	}
	w.Stage, err = multipartstage.New(multipartstage.Dependencies{
		Index: w.Multipart, Putter: w.Put, Dir: multipartDir, Clock: deps.Clock, Random: deps.Random, FS: deps.FS,
	})
	if err != nil {
		return fmt.Errorf("core: write: multipart stage: %w", err)
	}
	w.Sweeper, err = sweeper.New(sweeper.Dependencies{
		Jobs:     w.Jobs,
		Uploads:  w.Put,
		Deletes:  w.Remove,
		Due:      w.Cleanup,
		Drainer:  w.Drainer,
		Orphans:  w.Cleanup,
		Sleeper:  deps.Sleeper,
		Clock:    deps.Clock,
		Logger:   deps.Logger,
		Settings: deps.Sweeper,
	})
	if err != nil {
		return fmt.Errorf("core: write: sweeper: %w", err)
	}
	return nil
}

func (w *Write) Start(ctx context.Context) error {
	return w.Sweeper.Start(ctx)
}

func (w *Write) Close(ctx context.Context) error {
	return w.Sweeper.Close(ctx)
}

func validateWriteDependencies(deps WriteDependencies) error {
	missing := ""
	switch {
	case deps.Data == nil:
		missing = "data directory"
	case deps.Control == nil:
		missing = "control database"
	case deps.Database == nil:
		missing = "projection database"
	case deps.Projection == nil:
		missing = "projection"
	case deps.Sync == nil:
		missing = "sync"
	case deps.Telegram == nil:
		missing = "telegram"
	case deps.Sender == nil:
		missing = "sender"
	case deps.Prepaced == nil:
		missing = "pre-paced sender"
	case deps.Pacer == nil:
		missing = "pacer"
	case deps.Ring == nil:
		missing = "server key"
	case deps.ActorUserID <= 0:
		missing = "actor user id"
	case deps.FS == nil:
		missing = "filesystem"
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
		return fmt.Errorf("core: write: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}
