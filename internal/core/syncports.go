package core

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/syncer/reconcile"
	"github.com/PRIEYAN/ButterDB/internal/syncer/scan"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const encryptionConfigVersion = 1

var (
	_ scan.PeerResolver          = (*channelPeers)(nil)
	_ buckets.KeyMinter          = (*encryptionKeyMinter)(nil)
	_ buckets.KeyPresence        = (*activeKeyPresence)(nil)
	_ buckets.ActiveJobs         = noActiveJobs{}
	_ reconcile.BucketReferences = (*bucketReferences)(nil)
)

type peerResolver interface {
	ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type channelPeers struct {
	channels syncer.ChannelStore
	resolver peerResolver
}

func (p *channelPeers) ResolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	state, found, err := p.channels.Channel(ctx, channelID)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("core: load channel %d: %w", channelID, err)
	}
	if !found {
		return tgclient.InputPeer{}, fmt.Errorf("core: channel %d: %w", channelID, syncer.ErrUnknownChannel)
	}
	if state.AccessHash != 0 {
		return tgclient.InputPeer{ChannelID: channelID, AccessHash: state.AccessHash}, nil
	}
	peer, err := p.resolver.ResolveChannel(ctx, channelID)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("core: resolve channel %d: %w", channelID, err)
	}
	if peer.ChannelID != channelID {
		return tgclient.InputPeer{}, fmt.Errorf("core: resolve channel %d: %w", channelID, tgclient.ErrChannelUnavailable)
	}
	if peer.AccessHash == 0 {
		return tgclient.InputPeer{}, fmt.Errorf("core: resolve channel %d: %w", channelID, tgclient.ErrZeroAccessHash)
	}
	if err := p.channels.UpdateAccessHash(ctx, channelID, peer.AccessHash); err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("core: save access hash of channel %d: %w", channelID, err)
	}
	return peer, nil
}

type encryptionKeyMinter struct {
	ring   *keyring.Ring
	random io.Reader
}

func (m *encryptionKeyMinter) MintEncryptionConfig(_ context.Context, bucketChannelID int64) (wire.Op, error) {
	dek, err := keyring.GenerateKey(m.random)
	if err != nil {
		return wire.Op{}, fmt.Errorf("core: generate bucket key: %w", err)
	}
	defer dek.Clear()
	keyID, err := keyring.NewKeyID(m.random)
	if err != nil {
		return wire.Op{}, fmt.Errorf("core: generate key id: %w", err)
	}
	wrapped, check, err := m.ring.WrapDEK(dek, bucketChannelID, keyID)
	if err != nil {
		return wire.Op{}, fmt.Errorf("core: wrap bucket key: %w", err)
	}
	return wire.Op{
		Type:            wire.OpEncConfig,
		BucketChannelID: bucketChannelID,
		KeyID:           keyID,
		WrappedKey:      wrapped[:],
		KeyCheck:        check[:],
		KEKFingerprint:  m.ring.FingerprintString(),
		ConfigVersion:   encryptionConfigVersion,
	}, nil
}

type activeKeyPresence struct {
	keys        *bucketrepo.Repository
	fingerprint string
}

func (p *activeKeyPresence) HasActiveKey(ctx context.Context, bucketChannelID int64) (bool, error) {
	_, found, err := p.keys.ActiveKey(ctx, bucketChannelID, p.fingerprint)
	return found, err
}

// No job repository exists before phase 06, so no bucket can have an active job yet.
type noActiveJobs struct{}

func (noActiveJobs) HasActiveJobs(context.Context, int64) (bool, error) {
	return false, nil
}

type databaseReader interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type bucketReferences struct {
	db databaseReader
}

func (r *bucketReferences) ReferencedBucketChannels(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT channel_id FROM buckets ORDER BY channel_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("core: list bucket channels: %w", err)
	}
	return ids, nil
}
