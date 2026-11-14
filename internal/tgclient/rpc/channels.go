package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	primaryDialogFolderID  = 0
	archivedDialogFolderID = 1
)

type folderDialogsQuery struct {
	api      *tg.Client
	folderID int
}

func (q folderDialogsQuery) Query(ctx context.Context, request dialogs.Request) (tg.MessagesDialogsClass, error) {
	req := &tg.MessagesGetDialogsRequest{
		OffsetDate: request.OffsetDate,
		OffsetID:   request.OffsetID,
		OffsetPeer: request.OffsetPeer,
		Limit:      request.Limit,
	}
	// gotd's generated query builder does not mark the conditional folder flag present, including for folder 0.
	req.SetFolderID(q.folderID)
	return q.api.MessagesGetDialogs(ctx, req)
}

func folderQueries(api *tg.Client) []dialogs.Query {
	return []dialogs.Query{
		folderDialogsQuery{api: api, folderID: primaryDialogFolderID},
		folderDialogsQuery{api: api, folderID: archivedDialogFolderID},
	}
}

func (c *Client) CreateChannel(ctx context.Context, title, about string) (tgclient.InputPeer, error) {
	var peer tgclient.InputPeer
	err := c.write(ctx, tgclient.WriteChannel, "create channel", func(ctx context.Context, api *tg.Client) error {
		updates, err := api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
			Broadcast: true,
			Title:     title,
			About:     about,
		})
		if err != nil {
			return mapSendError(err)
		}
		created, ok := createdChannel(updates)
		if !ok {
			return fmt.Errorf("%w: create channel returned no channel", tgclient.ErrSendOutcomeUnknown)
		}
		peer = created
		return nil
	})
	if err != nil {
		return tgclient.InputPeer{}, err
	}
	return peer, nil
}

func createdChannel(updates tg.UpdatesClass) (tgclient.InputPeer, bool) {
	var chats []tg.ChatClass
	switch value := updates.(type) {
	case *tg.Updates:
		chats = value.Chats
	case *tg.UpdatesCombined:
		chats = value.Chats
	}
	for _, chat := range chats {
		channel, ok := chat.(*tg.Channel)
		if !ok || channel.ID == 0 || channel.AccessHash == 0 {
			continue
		}
		return tgclient.InputPeer{ChannelID: channel.ID, AccessHash: channel.AccessHash}, true
	}
	return tgclient.InputPeer{}, false
}

func (c *Client) ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error) {
	var channels []tgclient.Channel
	err := c.read(ctx, "list owned channels", func(ctx context.Context, api *tg.Client) error {
		var err error
		channels, err = collectOwnedChannels(ctx, folderQueries(api)...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return channels, nil
}

// An incomplete list could wrongly imply that a channel does not exist, so a failed page returns no partial result.
func collectOwnedChannels(ctx context.Context, queries ...dialogs.Query) ([]tgclient.Channel, error) {
	seen := make(map[int64]struct{})
	channels := make([]tgclient.Channel, 0)
	for _, query := range queries {
		iterator := dialogs.NewIterator(query, ownedDialogsBatchSize)
		for iterator.Next(ctx) {
			elem := iterator.Value()
			peer, ok := elem.Peer.(*tg.InputPeerChannel)
			if !ok {
				continue
			}
			channel, ok := elem.Entities.Channel(peer.ChannelID)
			if !ok || !isOwnedBroadcast(channel) {
				continue
			}
			if _, dup := seen[channel.ID]; dup {
				continue
			}
			seen[channel.ID] = struct{}{}
			channels = append(channels, tgclient.Channel{
				Peer:  tgclient.InputPeer{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Title: strings.TrimSpace(channel.Title),
			})
		}
		if err := iterator.Err(); err != nil {
			return nil, fmt.Errorf("list owned channels: %w", err)
		}
	}
	return channels, nil
}

func isOwnedBroadcast(channel *tg.Channel) bool {
	return channel.Creator && channel.Broadcast && !channel.Megagroup && !channel.Left && !channel.Min && channel.AccessHash != 0
}

func (c *Client) ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	if channelID <= 0 {
		return tgclient.InputPeer{}, fmt.Errorf("rpc: resolve channel: %w: invalid channel id", tgclient.ErrChannelUnavailable)
	}
	var peer tgclient.InputPeer
	err := c.read(ctx, "resolve channel", func(ctx context.Context, api *tg.Client) error {
		var err error
		peer, err = resolveFromDialogs(ctx, channelID, folderQueries(api)...)
		return err
	})
	if err != nil {
		return tgclient.InputPeer{}, err
	}
	return peer, nil
}

// channels.getChannels needs a known hash, so dialog entities are the only way to bootstrap one.
func resolveFromDialogs(ctx context.Context, channelID int64, queries ...dialogs.Query) (tgclient.InputPeer, error) {
	if err := ctx.Err(); err != nil {
		return tgclient.InputPeer{}, err
	}
	for _, query := range queries {
		iterator := dialogs.NewIterator(query, ownedDialogsBatchSize)
		for iterator.Next(ctx) {
			channel, ok := iterator.Value().Entities.Channel(channelID)
			if !ok || channel.Left || channel.Min || channel.AccessHash == 0 {
				continue
			}
			return tgclient.InputPeer{ChannelID: channelID, AccessHash: channel.AccessHash}, nil
		}
		if err := iterator.Err(); err != nil {
			return tgclient.InputPeer{}, fmt.Errorf("resolve channel dialogs: %w", err)
		}
	}
	return tgclient.InputPeer{}, fmt.Errorf("%w: no accessible dialog for channel", tgclient.ErrChannelUnavailable)
}
