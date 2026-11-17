package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/channelrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/syncer/difference"
	"github.com/PRIEYAN/ButterDB/internal/syncer/livesync"
	"github.com/PRIEYAN/ButterDB/internal/syncer/reconcile"
	"github.com/PRIEYAN/ButterDB/internal/syncer/scan"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

const registryChannelTitle = "butterDB registry"

type SyncDependencies struct {
	Database      *sqlitedb.DB
	Control       *sqlitedb.DB
	Projection    *Projection
	Telegram      tgclient.ChannelRPC
	Sender        tgclient.Sender
	Signals       livesync.SignalSource
	Ring          *keyring.Ring
	State         buckets.RegistryStateStore
	ActorUserID   int64
	DeploymentID  string
	RegistryTitle string
	LiveSync      livesync.Config
	Clock         clock.Clock
	Sleeper       flow.Sleeper
	Random        io.Reader
	Logger        *slog.Logger
}

type Sync struct {
	Gate        *scan.Gate
	Channels    syncer.ChannelStore
	Scanner     *scan.Scanner
	Difference  *difference.Service
	Coordinator *livesync.Coordinator
	Reconciler  *reconcile.Reconciler
	Orphans     *reconcile.OrphanFinder
	Buckets     *buckets.Service
	Registry    *buckets.RegistryProvisioner
	WriteGates  *buckets.WriteGates
}

func NewSync(deps SyncDependencies) (*Sync, error) {
	if err := validateSyncDependencies(deps); err != nil {
		return nil, err
	}
	channelRepo, err := channelrepo.New(deps.Database)
	if err != nil {
		return nil, fmt.Errorf("core: sync: channel repository: %w", err)
	}
	stores, err := newChannelStores(channelRepo)
	if err != nil {
		return nil, err
	}
	gate := scan.NewGate()
	ingester, err := difference.NewIngester(difference.IngesterDependencies{
		Projector: deps.Projection.Projector,
		Repairs:   deps.Projection.Rebuilder,
		Logger:    deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: sync: ingester: %w", err)
	}
	scanner, err := scan.NewScanner(scan.Dependencies{
		Channels:  stores,
		Cursors:   stores,
		Spool:     stores,
		Ingester:  ingester,
		Rebuilder: deps.Projection.Rebuilder,
		Writer:    deps.Database,
		Peers:     &channelPeers{channels: stores, resolver: deps.Telegram},
		History:   deps.Telegram,
		Gate:      gate,
		Sleeper:   deps.Sleeper,
		Clock:     deps.Clock,
		Random:    deps.Random,
		Logger:    deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: sync: scanner: %w", err)
	}
	assembled := &Sync{Gate: gate, Channels: stores, Scanner: scanner, WriteGates: buckets.NewWriteGates()}
	if err := assembled.assembleSyncers(deps, stores, ingester); err != nil {
		return nil, err
	}
	if err := assembled.assembleBuckets(deps, stores); err != nil {
		return nil, err
	}
	return assembled, nil
}

func (s *Sync) assembleSyncers(deps SyncDependencies, stores *channelStores, ingester syncer.OpIngester) error {
	diff, err := difference.New(difference.Dependencies{
		Gate:        s.Gate,
		Channels:    stores,
		Cursors:     stores,
		Deletions:   stores,
		Scanner:     s.Scanner,
		Ingester:    ingester,
		Differences: deps.Telegram,
		Writer:      deps.Database,
		Sleeper:     deps.Sleeper,
		Clock:       deps.Clock,
		Logger:      deps.Logger,
	})
	if err != nil {
		return fmt.Errorf("core: sync: difference: %w", err)
	}
	s.Difference = diff
	objects, err := objectrepo.New(deps.Database)
	if err != nil {
		return fmt.Errorf("core: sync: object repository: %w", err)
	}
	live, err := newLiveObjects(objects)
	if err != nil {
		return err
	}
	s.Reconciler, err = reconcile.NewReconciler(reconcile.Dependencies{
		Gate:      s.Gate,
		Channels:  stores,
		Live:      live,
		Deletions: stores,
		Cursors:   stores,
		Telegram:  deps.Telegram,
		Writer:    deps.Database,
		Clock:     deps.Clock,
		Logger:    deps.Logger,
	})
	if err != nil {
		return fmt.Errorf("core: sync: reconciler: %w", err)
	}
	s.Orphans, err = reconcile.NewOrphanFinder(reconcile.OrphanDependencies{
		Telegram: deps.Telegram,
		Buckets:  &bucketReferences{db: deps.Database},
		Logger:   deps.Logger,
	})
	if err != nil {
		return fmt.Errorf("core: sync: orphan finder: %w", err)
	}
	s.Coordinator, err = livesync.NewCoordinator(livesync.Dependencies{
		Signals:  deps.Signals,
		Syncer:   diff,
		Channels: stores,
		Clock:    deps.Clock,
		Logger:   deps.Logger,
	}, deps.LiveSync)
	if err != nil {
		return fmt.Errorf("core: sync: live sync: %w", err)
	}
	return nil
}

func (s *Sync) assembleBuckets(deps SyncDependencies, stores *channelStores) error {
	bucketRepo, err := bucketrepo.New(deps.Database)
	if err != nil {
		return fmt.Errorf("core: sync: bucket repository: %w", err)
	}
	publish := buckets.PublishDependencies{
		Channels:       stores,
		Resolver:       deps.Telegram,
		Messages:       deps.Telegram,
		Sender:         deps.Sender,
		Projector:      deps.Projection.Projector,
		Writer:         deps.Database,
		StableRandomID: send.StableRandomID,
		Clock:          deps.Clock,
		Logger:         deps.Logger,
		ActorUserID:    deps.ActorUserID,
	}
	s.Buckets, err = buckets.NewService(buckets.Dependencies{
		Publish:      publish,
		Buckets:      bucketRepo,
		Scanner:      s.Scanner,
		Gate:         s.Gate,
		Creator:      deps.Telegram,
		Keys:         &encryptionKeyMinter{ring: deps.Ring, random: deps.Random},
		KeyPresence:  &activeKeyPresence{keys: bucketRepo, fingerprint: deps.Ring.FingerprintString()},
		Jobs:         activeJobsFor(deps.Control),
		WriteGates:   s.WriteGates,
		Random:       deps.Random,
		DeploymentID: deps.DeploymentID,
	})
	if err != nil {
		return fmt.Errorf("core: sync: bucket service: %w", err)
	}
	s.Registry, err = buckets.NewRegistryProvisioner(buckets.RegistryDependencies{
		Publish:        publish,
		State:          deps.State,
		Scanner:        s.Scanner,
		Gate:           s.Gate,
		Creator:        deps.Telegram,
		DeploymentID:   deps.DeploymentID,
		KeyFingerprint: deps.Ring.FingerprintString(),
		Title:          deps.RegistryTitle,
	})
	if err != nil {
		return fmt.Errorf("core: sync: registry provisioner: %w", err)
	}
	return nil
}

func validateSyncDependencies(deps SyncDependencies) error {
	missing := ""
	switch {
	case deps.Database == nil:
		missing = "projection database"
	case deps.Projection == nil:
		missing = "projection"
	case deps.Telegram == nil:
		missing = "telegram"
	case deps.Sender == nil:
		missing = "sender"
	case deps.Signals == nil:
		missing = "signals"
	case deps.Ring == nil:
		missing = "server key"
	case deps.State == nil:
		missing = "registry state"
	case deps.DeploymentID == "":
		missing = "deployment id"
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
		return fmt.Errorf("core: sync: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}

func (s *Sync) RecordRegistry(ctx context.Context, state buckets.RegistryState) error {
	return recordRegistryChannel(ctx, s.Channels, state.ChannelID, state.AccessHash)
}

func recordRegistryChannel(ctx context.Context, channels syncer.ChannelStore, channelID, accessHash int64) error {
	row := syncer.ChannelState{ChannelID: channelID, AccessHash: accessHash, Kind: syncer.KindRegistry, Title: registryChannelTitle}
	if err := channels.UpsertChannel(ctx, row); err != nil {
		return fmt.Errorf("core: record registry channel %d: %w", channelID, err)
	}
	return nil
}

func activeJobsFor(control *sqlitedb.DB) buckets.ActiveJobs {
	if control == nil {
		return jobsUnavailable{}
	}
	return &activeJobs{db: control}
}
