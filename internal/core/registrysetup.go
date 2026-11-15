package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/channelrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/syncer/livesync"
	"github.com/PRIEYAN/ButterDB/internal/syncer/reconcile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	confirmCreateAnswer = "yes"
)

type RegistryOptions struct {
	RegistryChannelID int64
}

type RegistryOutcome struct {
	Existing     bool
	Created      bool
	Adopted      bool
	ChannelID    int64
	DeploymentID string
}

type RegistrySetupDependencies struct {
	Database      *sqlitedb.DB
	Control       *sqlitedb.DB
	Projection    *Projection
	Telegram      tgclient.ChannelRPC
	Sender        tgclient.Sender
	Signals       livesync.SignalSource
	Ring          *keyring.Ring
	PrimaryUserID int64
	StatePath     string
	Writer        *secretfile.Writer
	Terminal      Terminal
	Clock         clock.Clock
	Sleeper       flow.Sleeper
	Random        io.Reader
	Logger        *slog.Logger
}

func SetupRegistry(ctx context.Context, deps RegistrySetupDependencies, opts RegistryOptions) (RegistryOutcome, error) {
	if err := validateRegistrySetup(deps); err != nil {
		return RegistryOutcome{}, err
	}
	meta, err := newAdminMeta(deps.Control, deps.Random)
	if err != nil {
		return RegistryOutcome{}, err
	}
	state, err := newRegistryStateFile(deps.StatePath, deps.Writer, meta)
	if err != nil {
		return RegistryOutcome{}, err
	}
	run := &registrySetup{deps: deps, opts: opts, meta: meta, state: state}
	return run.run(ctx)
}

type registrySetup struct {
	deps  RegistrySetupDependencies
	opts  RegistryOptions
	meta  *adminMeta
	state *registryStateFile
}

func (r *registrySetup) run(ctx context.Context) (RegistryOutcome, error) {
	existing, found, err := r.state.LoadRegistryState(ctx)
	if err != nil {
		return RegistryOutcome{}, err
	}
	if found {
		return r.confirmExisting(ctx, existing)
	}
	discoverer, err := reconcile.NewDiscoverer(reconcile.DiscoveryDependencies{
		Telegram: r.deps.Telegram,
		Identity: reconcile.Identity{KeyFingerprint: r.deps.Ring.FingerprintString(), UserID: r.deps.PrimaryUserID},
		Logger:   r.deps.Logger,
	})
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: discoverer: %w", err)
	}
	candidates, err := discoverer.Discover(ctx)
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: %w", err)
	}
	if r.opts.RegistryChannelID == 0 && len(candidates) == 0 {
		return r.createWithConfirmation(ctx)
	}
	adoption, err := r.chooseAdoption(discoverer, candidates)
	if err != nil {
		return RegistryOutcome{}, err
	}
	return r.adopt(ctx, adoption)
}

func (r *registrySetup) confirmExisting(ctx context.Context, existing buckets.RegistryState) (RegistryOutcome, error) {
	checker, err := NewLocalStateChecker(LocalStateDependencies{
		State:       r.state,
		Fingerprint: r.deps.Ring.FingerprintString(),
		Database:    r.deps.Database,
		Accounts:    primaryAlwaysLinked{},
	})
	if err != nil {
		return RegistryOutcome{}, err
	}
	if err := checker.Check(ctx); err != nil {
		return RegistryOutcome{}, err
	}
	return RegistryOutcome{Existing: true, ChannelID: existing.ChannelID, DeploymentID: existing.DeploymentID}, nil
}

func (r *registrySetup) createWithConfirmation(ctx context.Context) (RegistryOutcome, error) {
	r.deps.Terminal.Say("No butterDB registry was found in this Telegram account.")
	answer, err := r.deps.Terminal.Prompt("type " + confirmCreateAnswer + " to create a new registry")
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: %w", err)
	}
	if strings.TrimSpace(answer) != confirmCreateAnswer {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: %w", ErrRegistryChoiceUnset)
	}
	return r.create(ctx)
}

func (r *registrySetup) create(ctx context.Context) (RegistryOutcome, error) {
	deploymentID, err := r.meta.EnsureDeploymentID(ctx)
	if err != nil {
		return RegistryOutcome{}, err
	}
	assembled, err := NewSync(SyncDependencies{
		Database:     r.deps.Database,
		Projection:   r.deps.Projection,
		Telegram:     r.deps.Telegram,
		Sender:       r.deps.Sender,
		Signals:      r.deps.Signals,
		Ring:         r.deps.Ring,
		State:        r.state,
		ActorUserID:  r.deps.PrimaryUserID,
		DeploymentID: deploymentID,
		Clock:        r.deps.Clock,
		Sleeper:      r.deps.Sleeper,
		Random:       r.deps.Random,
		Logger:       r.deps.Logger,
	})
	if err != nil {
		return RegistryOutcome{}, err
	}
	registry, err := assembled.Registry.Ensure(ctx)
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: %w", err)
	}
	return RegistryOutcome{Created: true, ChannelID: registry.ChannelID, DeploymentID: deploymentID}, nil
}

func (r *registrySetup) chooseAdoption(discoverer *reconcile.Discoverer, candidates []reconcile.Candidate) (reconcile.Adoption, error) {
	if r.opts.RegistryChannelID > 0 {
		return adoptionOf(discoverer, candidates, r.opts.RegistryChannelID)
	}
	r.printCandidates(candidates)
	if len(reconcile.Eligible(candidates)) == 0 {
		return reconcile.Adoption{}, keyFailure(ReasonForeignRegistry, "every registry in this account belongs to another key or user")
	}
	answer, err := r.deps.Terminal.Prompt("channel id of the registry to adopt")
	if err != nil {
		return reconcile.Adoption{}, fmt.Errorf("core: registry setup: %w", err)
	}
	channelID, err := strconv.ParseInt(strings.TrimSpace(answer), 10, 64)
	if err != nil || channelID <= 0 {
		return reconcile.Adoption{}, fmt.Errorf("core: registry setup: %w", ErrRegistryChoiceUnset)
	}
	return adoptionOf(discoverer, candidates, channelID)
}

func adoptionOf(discoverer *reconcile.Discoverer, candidates []reconcile.Candidate, channelID int64) (reconcile.Adoption, error) {
	adoption, err := discoverer.Adopt(candidates, channelID)
	if err != nil {
		return reconcile.Adoption{}, fmt.Errorf("core: registry setup: %w: %w", ErrKeyOrRegistry, err)
	}
	return adoption, nil
}

func (r *registrySetup) printCandidates(candidates []reconcile.Candidate) {
	terminal := r.deps.Terminal
	if len(candidates) == 0 {
		return
	}
	terminal.Say("Registry channels found in this Telegram account:")
	for _, c := range candidates {
		line := fmt.Sprintf("  %d  %q  created %s  deployment %s  format %d  min reader %d  live buckets %d",
			c.Peer.ChannelID, c.Title, time.Unix(c.CreatedAt, 0).UTC().Format(time.DateOnly), c.DeploymentID,
			c.FormatVersion, c.MinReaderVersion, c.LiveBuckets)
		if !c.Eligible() {
			line += "  foreign (" + string(c.Foreign) + "), cannot be adopted"
		}
		terminal.Say(line)
	}
}

func (r *registrySetup) adopt(ctx context.Context, adoption reconcile.Adoption) (RegistryOutcome, error) {
	if adoption.MinReaderVersion > wire.FormatVersion {
		return RegistryOutcome{}, incompatibleFailure(ReasonFormatTooNew, "the registry needs a newer binary")
	}
	if err := r.meta.SetDeploymentID(ctx, adoption.DeploymentID); err != nil {
		return RegistryOutcome{}, err
	}
	repo, err := channelrepo.New(r.deps.Database)
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: channel repository: %w", err)
	}
	stores, err := newChannelStores(repo)
	if err != nil {
		return RegistryOutcome{}, err
	}
	row := syncer.ChannelState{ChannelID: adoption.RegistryChannelID, AccessHash: adoption.RegistryAccessHash, Kind: syncer.KindRegistry}
	if err := stores.UpsertChannel(ctx, row); err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: record registry channel: %w", err)
	}
	err = r.state.SaveRegistryState(ctx, buckets.RegistryState{
		ChannelID:      adoption.RegistryChannelID,
		AccessHash:     adoption.RegistryAccessHash,
		MarkerMsgID:    adoption.RegistryMarkerMsgID,
		PrimaryUserID:  adoption.PrimaryUserID,
		KeyFingerprint: adoption.ServerKeyFingerprint,
		DeploymentID:   adoption.DeploymentID,
	})
	if err != nil {
		return RegistryOutcome{}, fmt.Errorf("core: registry setup: %w", err)
	}
	return RegistryOutcome{Adopted: true, ChannelID: adoption.RegistryChannelID, DeploymentID: adoption.DeploymentID}, nil
}

func validateRegistrySetup(deps RegistrySetupDependencies) error {
	missing := ""
	switch {
	case deps.Database == nil:
		missing = "projection database"
	case deps.Control == nil:
		missing = "control database"
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
	case deps.PrimaryUserID <= 0:
		missing = "primary user"
	case deps.StatePath == "":
		missing = "state path"
	case deps.Writer == nil:
		missing = "secret writer"
	case deps.Terminal == nil:
		missing = "terminal"
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
		return fmt.Errorf("core: registry setup: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}
