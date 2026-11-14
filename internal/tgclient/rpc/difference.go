package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) GetChannelDifference(ctx context.Context, peer tgclient.InputPeer, pts int, limit int) (tgclient.ChannelDifference, error) {
	if err := requirePeer("get channel difference", peer); err != nil {
		return tgclient.ChannelDifference{}, err
	}
	if limit <= 0 || limit > differencePageLimit {
		limit = differencePageLimit
	}
	var out tgclient.ChannelDifference
	err := c.read(ctx, "get channel difference", func(ctx context.Context, api *tg.Client) error {
		result, err := api.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{
			Channel: inputChannel(peer),
			Filter:  &tg.ChannelMessagesFilterEmpty{},
			Pts:     pts,
			Limit:   limit,
		})
		if err != nil {
			return err
		}
		out, err = differenceFromTG(result)
		return err
	})
	if err != nil {
		return tgclient.ChannelDifference{}, err
	}
	return out, nil
}

func differenceFromTG(result tg.UpdatesChannelDifferenceClass) (tgclient.ChannelDifference, error) {
	switch diff := result.(type) {
	case *tg.UpdatesChannelDifferenceEmpty:
		return tgclient.ChannelDifference{Pts: diff.Pts, Final: diff.Final}, nil
	case *tg.UpdatesChannelDifference:
		return pageDifference(diff), nil
	case *tg.UpdatesChannelDifferenceTooLong:
		out := tgclient.ChannelDifference{Final: true, TooLong: true}
		if dialog, ok := diff.Dialog.(*tg.Dialog); ok {
			out.Pts = dialog.Pts
		}
		return out, nil
	default:
		return tgclient.ChannelDifference{}, unexpectedType("get channel difference", result)
	}
}

func pageDifference(diff *tg.UpdatesChannelDifference) tgclient.ChannelDifference {
	out := tgclient.ChannelDifference{Pts: diff.Pts, Final: diff.Final}
	out.Messages = historyFromTG(diff.NewMessages)
	for _, update := range diff.OtherUpdates {
		deleted, ok := update.(*tg.UpdateDeleteChannelMessages)
		if !ok {
			continue
		}
		for _, id := range deleted.Messages {
			out.DeletedMessageIDs = append(out.DeletedMessageIDs, int64(id))
		}
	}
	return out
}

func (c *Client) GetChannelPts(ctx context.Context, peer tgclient.InputPeer) (int, error) {
	if err := requirePeer("get channel pts", peer); err != nil {
		return 0, err
	}
	var pts int
	err := c.read(ctx, "get channel pts", func(ctx context.Context, api *tg.Client) error {
		full, err := api.ChannelsGetFullChannel(ctx, inputChannel(peer))
		if err != nil {
			return err
		}
		channelFull, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			return unexpectedType("get channel pts", full.FullChat)
		}
		pts = channelFull.Pts
		return nil
	})
	if err != nil {
		return 0, err
	}
	return pts, nil
}
