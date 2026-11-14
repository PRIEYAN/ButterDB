package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	if err := requirePeer("get history", peer); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > historyPageLimit {
		limit = historyPageLimit
	}
	var out []tgclient.HistoryMessage
	err := c.read(ctx, "get history", func(ctx context.Context, api *tg.Client) error {
		result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     inputPeerChannel(peer),
			OffsetID: int(offsetID),
			Limit:    limit,
		})
		if err != nil {
			return err
		}
		out = historyFromTG(messagesOf(result))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error) {
	if err := requirePeer("get messages", peer); err != nil {
		return nil, err
	}
	requested := distinctPositiveIDs(msgIDs)
	if len(requested) == 0 {
		return nil, nil
	}
	found := make(map[int64]tgclient.HistoryMessage, len(requested))
	err := c.read(ctx, "get messages", func(ctx context.Context, api *tg.Client) error {
		for start := 0; start < len(requested); start += channelsGetMessagesLimit {
			end := min(start+channelsGetMessagesLimit, len(requested))
			if err := fetchBatch(ctx, api, peer, requested[start:end], found); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]tgclient.HistoryMessage, 0, len(found))
	for _, id := range requested {
		if message, ok := found[id]; ok {
			out = append(out, message)
		}
	}
	return out, nil
}

func fetchBatch(ctx context.Context, api *tg.Client, peer tgclient.InputPeer, ids []int64, found map[int64]tgclient.HistoryMessage) error {
	inputs := make([]tg.InputMessageClass, 0, len(ids))
	for _, id := range ids {
		inputs = append(inputs, &tg.InputMessageID{ID: int(id)})
	}
	result, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: inputChannel(peer), ID: inputs})
	if err != nil {
		return err
	}
	for _, raw := range messagesOf(result) {
		if _, deleted := raw.(*tg.MessageEmpty); deleted {
			continue
		}
		if message, ok := historyMessageFromTG(raw); ok {
			found[message.MsgID] = message
		}
	}
	return nil
}

func distinctPositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func messagesOf(result tg.MessagesMessagesClass) []tg.MessageClass {
	switch value := result.(type) {
	case *tg.MessagesMessages:
		return value.Messages
	case *tg.MessagesMessagesSlice:
		return value.Messages
	case *tg.MessagesChannelMessages:
		return value.Messages
	default:
		return nil
	}
}

func historyFromTG(messages []tg.MessageClass) []tgclient.HistoryMessage {
	var out []tgclient.HistoryMessage
	for _, raw := range messages {
		if message, ok := historyMessageFromTG(raw); ok {
			out = append(out, message)
		}
	}
	return out
}

func placeholderMessage(msg tg.MessageClass) (msgID int64, date int64, ok bool) {
	switch m := msg.(type) {
	case *tg.MessageService:
		return int64(m.ID), int64(m.Date), true
	case *tg.MessageEmpty:
		return int64(m.ID), 0, true
	default:
		return 0, 0, false
	}
}

func historyMessageFromTG(msg tg.MessageClass) (tgclient.HistoryMessage, bool) {
	full, ok := msg.(*tg.Message)
	if !ok {
		// A non-positive id cannot be paged from, so reporting one would let a caller loop forever.
		if id, date, isPlaceholder := placeholderMessage(msg); isPlaceholder && id > 0 {
			return tgclient.HistoryMessage{MsgID: id, Date: date, Placeholder: true}, true
		}
		return tgclient.HistoryMessage{}, false
	}
	out := tgclient.HistoryMessage{
		MsgID: int64(full.ID),
		Date:  int64(full.Date),
		Text:  strings.TrimRight(full.Message, "\r\n"),
	}
	if from, isUser := full.FromID.(*tg.PeerUser); isUser {
		out.FromID = from.UserID
	}
	applyDocument(&out, full)
	return out, true
}

func applyDocument(out *tgclient.HistoryMessage, full *tg.Message) {
	media, ok := full.Media.(*tg.MessageMediaDocument)
	if !ok {
		return
	}
	out.HasMedia = true
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return
	}
	out.MediaSize = doc.Size
	for _, attribute := range doc.Attributes {
		if name, isName := attribute.(*tg.DocumentAttributeFilename); isName {
			out.DocumentName = name.FileName
		}
	}
}

func unexpectedType(operation string, value any) error {
	return fmt.Errorf("rpc: %s: unexpected result type %T", operation, value)
}
