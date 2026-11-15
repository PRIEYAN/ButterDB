package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/sweeper"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/readpath/blockcache"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer/livesync"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var _ lifecycle.Component = (*Runtime)(nil)

type RuntimeDependencies struct {
	Config       config.Config
	Data         *datadir.Dir
	Control      *sqlitedb.DB
	Database     *sqlitedb.DB
	Ring         *keyring.Ring
	State        buckets.RegistryStateStore
	Telegram     tgclient.ChannelRPC
	Sender       tgclient.Sender
	Prepaced     tgclient.Sender
	Origin       tgclient.RangeReader
	Signals      livesync.SignalSource
	Pacer        admission.Pacer
	ActorUserID  int64
	DeploymentID string
	FS           fsx.FS
	Files        blockcache.Files
	Clock        clock.Clock
	Sleeper      flow.Sleeper
	Random       io.Reader
	Logger       *slog.Logger
	Sweeper      sweeper.Config
}

type Runtime struct {
	Projection *Projection
	Sync       *Sync
	Write      *Write
	Read       *Read
	S3         *S3

	group *lifecycle.Group
}

func NewRuntime(deps RuntimeDependencies) (*Runtime, error) {
	if err := validateRuntimeDependencies(deps); err != nil {
		return nil, err
	}
	rt := &Runtime{}
	if err := rt.assemble(deps); err != nil {
		return nil, err
	}
	rt.group = lifecycle.NewGroup(deps.Config.Shutdown.ComponentTimeout.Std())
	rt.group.Add("read", rt.Read)
	rt.group.Add("write", rt.Write)
	rt.group.Add("livesync", rt.Sync.Coordinator)
	rt.group.Add("multipart-expiry", &multipartExpiry{
		stage: rt.Write.Stage, idle: deps.Config.S3.MultipartIdleExpiry.Std(), sleeper: deps.Sleeper, logger: deps.Logger,
	})
	rt.group.Add("initial-sync", &authoritativeSync{
		scanner: rt.Sync.Scanner, readiness: rt.S3, sleeper: deps.Sleeper, logger: deps.Logger,
	})
	rt.group.Add("s3", rt.S3)
	return rt, nil
}

func (rt *Runtime) assemble(deps RuntimeDependencies) error {
	var err error
	if rt.Projection, err = NewProjection(ProjectionDependencies{Logger: deps.Logger}); err != nil {
		return err
	}
	rt.Sync, err = NewSync(SyncDependencies{
		Database: deps.Database, Control: deps.Control, Projection: rt.Projection, Telegram: deps.Telegram, Sender: deps.Sender,
		Signals: deps.Signals, Ring: deps.Ring, State: deps.State, ActorUserID: deps.ActorUserID, DeploymentID: deps.DeploymentID,
		Clock: deps.Clock, Sleeper: deps.Sleeper, Random: deps.Random, Logger: deps.Logger,
	})
	if err != nil {
		return err
	}
	rt.Write, err = NewWrite(WriteDependencies{
		Config: deps.Config, Data: deps.Data, Control: deps.Control, Database: deps.Database, Projection: rt.Projection, Sync: rt.Sync,
		Telegram: deps.Telegram, Sender: deps.Sender, Prepaced: deps.Prepaced, Pacer: deps.Pacer, Ring: deps.Ring,
		ActorUserID: deps.ActorUserID, FS: deps.FS, Clock: deps.Clock, Sleeper: deps.Sleeper, Random: deps.Random,
		Logger: deps.Logger, Sweeper: deps.Sweeper,
	})
	if err != nil {
		return err
	}
	rt.Read, err = NewRead(ReadDependencies{
		Config: deps.Config, Database: deps.Database, Sync: rt.Sync, Telegram: deps.Telegram, Origin: deps.Origin, Ring: deps.Ring,
		Files: deps.Files, Clock: deps.Clock, Sleeper: deps.Sleeper, Random: deps.Random, Logger: deps.Logger,
	})
	if err != nil {
		return err
	}
	rt.S3, err = NewS3(S3Dependencies{
		Config: deps.Config, Control: deps.Control, Database: deps.Database, Ring: deps.Ring, Write: rt.Write, Read: rt.Read,
		Clock: deps.Clock, Random: deps.Random, Logger: deps.Logger,
	})
	return err
}

func (rt *Runtime) Start(ctx context.Context) error {
	if err := rt.group.Start(ctx); err != nil {
		return fmt.Errorf("core: runtime: %w", err)
	}
	return nil
}

func (rt *Runtime) Close(ctx context.Context) error {
	if err := rt.group.Close(ctx); err != nil {
		return fmt.Errorf("core: runtime: close: %w", err)
	}
	return nil
}

func validateRuntimeDependencies(deps RuntimeDependencies) error {
	missing := ""
	switch {
	case deps.Data == nil:
		missing = "data directory"
	case deps.Control == nil:
		missing = "control database"
	case deps.Database == nil:
		missing = "projection database"
	case deps.Ring == nil:
		missing = "server key"
	case deps.State == nil:
		missing = "registry state"
	case deps.Telegram == nil:
		missing = "telegram"
	case deps.Sender == nil:
		missing = "sender"
	case deps.Prepaced == nil:
		missing = "pre-paced sender"
	case deps.Origin == nil:
		missing = "range origin"
	case deps.Signals == nil:
		missing = "signals"
	case deps.Pacer == nil:
		missing = "pacer"
	case deps.DeploymentID == "":
		missing = "deployment id"
	case deps.FS == nil:
		missing = "filesystem"
	case deps.Files == nil:
		missing = "cache files"
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
		return fmt.Errorf("core: runtime: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}
