package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/channelrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var (
	_ syncer.ChannelStore    = (*channelStores)(nil)
	_ syncer.SyncCursorStore = (*channelStores)(nil)
	_ syncer.ScanSpool       = (*channelStores)(nil)
	_ syncer.DeletionStore   = (*channelStores)(nil)
	_ syncer.LiveObjects     = (*liveObjects)(nil)
)

type channelStores struct {
	repo *channelrepo.Repository
}

func newChannelStores(repo *channelrepo.Repository) (*channelStores, error) {
	if repo == nil {
		return nil, fmt.Errorf("core: channel stores: %w: channel repository", errs.ErrMissingDependency)
	}
	return &channelStores{repo: repo}, nil
}

func (s *channelStores) Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error) {
	channel, found, err := s.repo.GetChannel(ctx, channelID)
	if err != nil || !found {
		return syncer.ChannelState{}, false, err
	}
	return stateOf(channel), true, nil
}

func (s *channelStores) Channels(ctx context.Context) ([]syncer.ChannelState, error) {
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]syncer.ChannelState, 0, len(channels))
	for _, channel := range channels {
		states = append(states, stateOf(channel))
	}
	return states, nil
}

func (s *channelStores) UpsertChannel(ctx context.Context, state syncer.ChannelState) error {
	return s.repo.UpsertChannel(ctx, channelrepo.Channel{
		ChannelID:  state.ChannelID,
		AccessHash: state.AccessHash,
		Kind:       channelrepo.Kind(state.Kind),
		Title:      state.Title,
	})
}

func (s *channelStores) UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error {
	return unknownChannelOnMissing(s.repo.UpdateAccessHash(ctx, channelID, accessHash))
}

func (s *channelStores) SetSyncCursorTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	return unknownChannelOnMissing(s.repo.SetSyncCursorTx(ctx, tx, channelID, lastSyncedMsg, pts))
}

func (s *channelStores) MarkInitialSyncDoneTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error {
	return unknownChannelOnMissing(s.repo.MarkInitialSyncDoneTx(ctx, tx, channelID, lastSyncedMsg, pts))
}

func (s *channelStores) SetNeedsProjectionRebuild(ctx context.Context, channelID int64, needed bool) error {
	return unknownChannelOnMissing(s.repo.SetNeedsProjectionRebuild(ctx, channelID, needed))
}

func (s *channelStores) SetLastReconciledAt(ctx context.Context, channelID, at int64) error {
	return unknownChannelOnMissing(s.repo.SetLastReconciledAt(ctx, channelID, at))
}

func (s *channelStores) ClearSpool(ctx context.Context, channelID int64) error {
	return s.repo.ClearSpool(ctx, channelID)
}

func (s *channelStores) SpoolPage(ctx context.Context, channelID int64, msgs []tgclient.HistoryMessage) error {
	rows := make([]channelrepo.SpoolMessage, 0, len(msgs))
	for _, msg := range msgs {
		rows = append(rows, channelrepo.SpoolMessage{
			MsgID:        msg.MsgID,
			Date:         msg.Date,
			FromID:       msg.FromID,
			Text:         msg.Text,
			HasMedia:     msg.HasMedia,
			MediaSize:    msg.MediaSize,
			DocumentName: msg.DocumentName,
		})
	}
	return s.repo.SpoolPage(ctx, channelID, rows)
}

func (s *channelStores) SpooledPageTx(ctx context.Context, tx projection.Tx, channelID, afterMsgID int64, limit int) ([]tgclient.HistoryMessage, error) {
	rows, err := s.repo.SpooledPageTx(ctx, tx, channelID, afterMsgID, limit)
	if err != nil {
		return nil, err
	}
	msgs := make([]tgclient.HistoryMessage, 0, len(rows))
	for _, row := range rows {
		msgs = append(msgs, tgclient.HistoryMessage{
			MsgID:        row.MsgID,
			Date:         row.Date,
			FromID:       row.FromID,
			Text:         row.Text,
			HasMedia:     row.HasMedia,
			MediaSize:    row.MediaSize,
			DocumentName: row.DocumentName,
			Placeholder:  false,
		})
	}
	return msgs, nil
}

func (s *channelStores) RecordDeletedTx(ctx context.Context, tx projection.Tx, channelID int64, msgIDs []int64, source syncer.DeletionSource, recordedAt int64) ([]string, error) {
	return s.repo.RecordDeletedTx(ctx, tx, channelID, msgIDs, channelrepo.DeletionSource(source), recordedAt)
}

func stateOf(channel channelrepo.Channel) syncer.ChannelState {
	return syncer.ChannelState{
		ChannelID:              channel.ChannelID,
		AccessHash:             channel.AccessHash,
		Kind:                   syncer.ChannelKind(channel.Kind),
		Title:                  channel.Title,
		Deleted:                channel.Deleted,
		LastSyncedMsg:          channel.LastSyncedMsg,
		Pts:                    channel.Pts,
		InitialSyncDone:        channel.InitialSyncDone,
		NeedsProjectionRebuild: channel.NeedsProjectionRebuild,
		LastReconciledAt:       channel.LastReconciledAt,
	}
}

func unknownChannelOnMissing(err error) error {
	if errors.Is(err, channelrepo.ErrChannelNotFound) {
		return fmt.Errorf("%w: %w", syncer.ErrUnknownChannel, err)
	}
	return err
}

type liveMessageSource interface {
	LiveMessageIDs(ctx context.Context, channelID int64) ([]objectrepo.MessageRefs, error)
}

type liveObjects struct {
	objects liveMessageSource
}

func newLiveObjects(objects liveMessageSource) (*liveObjects, error) {
	if objects == nil {
		return nil, fmt.Errorf("core: live objects: %w: object repository", errs.ErrMissingDependency)
	}
	return &liveObjects{objects: objects}, nil
}

func (l *liveObjects) LiveMessageIDs(ctx context.Context, channelID int64) ([]syncer.LiveObjectRef, error) {
	refs, err := l.objects.LiveMessageIDs(ctx, channelID)
	if err != nil {
		return nil, err
	}
	out := make([]syncer.LiveObjectRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, syncer.LiveObjectRef{Key: ref.Key, PutMsgID: ref.PutMsgID, BackingMsgIDs: ref.BackingMsgIDs})
	}
	return out, nil
}
