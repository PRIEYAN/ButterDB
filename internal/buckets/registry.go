package buckets

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	registryChannelTitle   = "butterDB registry"
	writerSetup            = "sys.setup"
	purposeRegistryMarker  = "reg:1"
	initialMinReaderFormat = 1
)

type RegistryProvisioner struct {
	publisher      *opPublisher
	state          RegistryStateStore
	scanner        EmptyChannelSyncer
	gate           ChannelGate
	creator        ChannelCreator
	deploymentID   string
	keyFingerprint string
}

func NewRegistryProvisioner(deps RegistryDependencies) (*RegistryProvisioner, error) {
	publisher, err := newOpPublisher(deps.Publish)
	if err != nil {
		return nil, err
	}
	switch {
	case deps.State == nil:
		return nil, fmt.Errorf("buckets: %w: registry state", errs.ErrMissingDependency)
	case deps.Scanner == nil:
		return nil, fmt.Errorf("buckets: %w: scanner", errs.ErrMissingDependency)
	case deps.Gate == nil:
		return nil, fmt.Errorf("buckets: %w: channel gate", errs.ErrMissingDependency)
	case deps.Creator == nil:
		return nil, fmt.Errorf("buckets: %w: channel creator", errs.ErrMissingDependency)
	case deps.DeploymentID == "":
		return nil, fmt.Errorf("buckets: %w: deployment id", errs.ErrMissingDependency)
	case deps.KeyFingerprint == "":
		return nil, fmt.Errorf("buckets: %w: key fingerprint", errs.ErrMissingDependency)
	}
	return &RegistryProvisioner{
		publisher:      publisher,
		state:          deps.State,
		scanner:        deps.Scanner,
		gate:           deps.Gate,
		creator:        deps.Creator,
		deploymentID:   deps.DeploymentID,
		keyFingerprint: deps.KeyFingerprint,
	}, nil
}

func (p *RegistryProvisioner) Ensure(ctx context.Context) (RegistryState, error) {
	existing, found, err := p.state.LoadRegistryState(ctx)
	if err != nil {
		return RegistryState{}, fmt.Errorf("buckets: load registry state: %w", err)
	}
	if found {
		return existing, nil
	}
	channel, resumed, err := p.registryChannel(ctx)
	if err != nil {
		return RegistryState{}, err
	}
	if err := p.markEmptyAuthoritative(ctx, channel); err != nil {
		return RegistryState{}, err
	}
	markerMsgID, err := p.publisher.publish(ctx, publication{
		channel: channel,
		op:      p.markerOp(),
		seed:    p.deploymentID,
		purpose: purposeRegistryMarker,
		resumed: resumed,
	})
	if err != nil {
		return RegistryState{}, fmt.Errorf("buckets: publish registry marker: %w", err)
	}
	state := RegistryState{
		ChannelID:      channel.ChannelID,
		AccessHash:     channel.AccessHash,
		MarkerMsgID:    markerMsgID,
		PrimaryUserID:  p.publisher.actor,
		KeyFingerprint: p.keyFingerprint,
		DeploymentID:   p.deploymentID,
	}
	if err := p.state.SaveRegistryState(ctx, state); err != nil {
		return RegistryState{}, fmt.Errorf("buckets: save registry state: %w", err)
	}
	return state, nil
}

func (p *RegistryProvisioner) registryChannel(ctx context.Context) (syncer.ChannelState, bool, error) {
	states, err := p.publisher.channels.Channels(ctx)
	if err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: list channels: %w", err)
	}
	for _, state := range states {
		if state.Kind != syncer.KindRegistry || state.Deleted {
			continue
		}
		if state.AccessHash == 0 {
			peer, err := p.publisher.refreshPeer(ctx, state.ChannelID)
			if err != nil {
				return syncer.ChannelState{}, false, err
			}
			state.AccessHash = peer.AccessHash
		}
		return state, true, nil
	}
	about := fmt.Sprintf("butterDB registry %s. Do not post, edit or delete messages here.", p.deploymentID)
	peer, err := p.creator.CreateChannel(ctx, registryChannelTitle, about)
	if err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: create registry channel: %w", err)
	}
	row := syncer.ChannelState{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash, Kind: syncer.KindRegistry, Title: registryChannelTitle}
	if err := p.publisher.channels.UpsertChannel(ctx, row); err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: record registry channel %d: %w", peer.ChannelID, err)
	}
	return row, false, nil
}

func (p *RegistryProvisioner) markEmptyAuthoritative(ctx context.Context, channel syncer.ChannelState) error {
	return markChannelEmptyAuthoritative(ctx, p.publisher.channels, p.gate, p.scanner, channel.ChannelID)
}

func (p *RegistryProvisioner) markerOp() wire.Op {
	return wire.Op{
		Type:             wire.OpRegistry,
		DeploymentID:     p.deploymentID,
		FormatVersion:    wire.FormatVersion,
		MinReaderVersion: initialMinReaderFormat,
		KEKFingerprint:   p.keyFingerprint,
		PrimaryUserID:    p.publisher.actor,
		Writer:           writerSetup,
		Timestamp:        p.publisher.clock.Now().Unix(),
	}
}
