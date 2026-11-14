package rangeio

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const channelsGetMessagesLimit = 100

func (r *Reader) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	refs, err := r.ResolveDocuments(ctx, peer, []int64{msgID})
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	return refs[0], nil
}

func (r *Reader) ResolveDocuments(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.DocumentRef, error) {
	if peer.ChannelID == 0 || peer.AccessHash == 0 {
		return nil, fmt.Errorf("rangeio: resolve documents: %w", tgclient.ErrZeroAccessHash)
	}
	refs := make([]tgclient.DocumentRef, 0, len(msgIDs))
	for start := 0; start < len(msgIDs); start += channelsGetMessagesLimit {
		batch := msgIDs[start:min(start+channelsGetMessagesLimit, len(msgIDs))]
		messages, err := r.channelMessages(ctx, peer, batch)
		if err != nil {
			return nil, err
		}
		batchRefs, err := documentRefsInOrder(peer, batch, messages)
		if err != nil {
			return nil, err
		}
		refs = append(refs, batchRefs...)
	}
	return refs, nil
}

// Telegram returns found messages in no particular order and substitutes an empty placeholder for deleted ones.
func (r *Reader) channelMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) (map[int64]tg.MessageClass, error) {
	ids := make([]tg.InputMessageClass, 0, len(msgIDs))
	for _, id := range msgIDs {
		if id <= 0 || id > int64(1<<31-1) {
			return nil, fmt.Errorf("rangeio: message %d: %w", id, tgclient.ErrMessageNotFound)
		}
		ids = append(ids, &tg.InputMessageID{ID: int(id)})
	}
	var result tg.MessagesMessagesClass
	err := r.invoker.Invoke(ctx, func(ctx context.Context, api *tg.Client) error {
		var err error
		result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash},
			ID:      ids,
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("rangeio: get messages: %w", classifyChannelError(normalizeError(err)))
	}
	found := make(map[int64]tg.MessageClass, len(msgIDs))
	if messages, ok := result.(*tg.MessagesChannelMessages); ok {
		for _, msg := range messages.Messages {
			found[int64(msg.GetID())] = msg
		}
	}
	return found, nil
}

func classifyChannelError(err error) error {
	rpcErr, ok := tgerr.As(err)
	if ok && (rpcErr.Type == "CHANNEL_INVALID" || rpcErr.Type == "CHANNEL_PRIVATE") {
		return errors.Join(tgclient.ErrChannelUnavailable, err)
	}
	return err
}

func documentRefsInOrder(peer tgclient.InputPeer, msgIDs []int64, messages map[int64]tg.MessageClass) ([]tgclient.DocumentRef, error) {
	refs := make([]tgclient.DocumentRef, 0, len(msgIDs))
	for _, id := range msgIDs {
		msg, ok := messages[id]
		if !ok {
			return nil, fmt.Errorf("rangeio: message %d: %w", id, tgclient.ErrMessageNotFound)
		}
		ref, err := documentRefFromMessage(peer, id, msg)
		if err != nil {
			return nil, fmt.Errorf("rangeio: message %d: %w", id, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func documentRefFromMessage(peer tgclient.InputPeer, msgID int64, msg tg.MessageClass) (tgclient.DocumentRef, error) {
	full, ok := msg.(*tg.Message)
	if !ok {
		return tgclient.DocumentRef{}, tgclient.ErrMessageNotFound
	}
	media, ok := full.Media.(*tg.MessageMediaDocument)
	if !ok {
		return tgclient.DocumentRef{}, ErrNotDocument
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return tgclient.DocumentRef{}, ErrNotDocument
	}
	return tgclient.DocumentRef{
		Peer:          peer,
		MsgID:         msgID,
		Size:          doc.Size,
		DocumentID:    doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: append([]byte(nil), doc.FileReference...),
		DCID:          doc.DCID,
	}, nil
}
