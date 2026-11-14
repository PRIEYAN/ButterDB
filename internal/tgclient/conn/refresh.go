package conn

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const channelsGetMessagesLimit = 100

const refreshTimeout = 30 * time.Second

type refreshedDocument struct {
	documentID    int64
	accessHash    int64
	fileReference []byte
	dcID          int
}

func (c *Connection) RefreshDocument(ctx context.Context, peerChannelID, peerAccessHash, msgID int64) (documentID, accessHash int64, fileReference []byte, dcID int, err error) {
	key := strconv.FormatInt(peerChannelID, 10) + ":" + strconv.FormatInt(msgID, 10)
	peer := tgclient.InputPeer{ChannelID: peerChannelID, AccessHash: peerAccessHash}
	fresh, err := c.refreshes.do(ctx, key, func(flightCtx context.Context) (refreshedDocument, error) {
		ref, err := c.ResolveDocument(flightCtx, peer, msgID)
		if err != nil {
			return refreshedDocument{}, err
		}
		return refreshedDocument{documentID: ref.DocumentID, accessHash: ref.AccessHash, fileReference: ref.FileReference, dcID: ref.DCID}, nil
	})
	if err != nil {
		return 0, 0, nil, 0, err
	}
	return fresh.documentID, fresh.accessHash, append([]byte(nil), fresh.fileReference...), fresh.dcID, nil
}

func (c *Connection) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	refs, err := c.ResolveDocuments(ctx, peer, []int64{msgID})
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	return refs[0], nil
}

func (c *Connection) ResolveDocuments(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.DocumentRef, error) {
	refs := make([]tgclient.DocumentRef, 0, len(msgIDs))
	err := c.Invoke(ctx, func(ctx context.Context, api *tg.Client) error {
		for start := 0; start < len(msgIDs); start += channelsGetMessagesLimit {
			batch := msgIDs[start:min(start+channelsGetMessagesLimit, len(msgIDs))]
			messages, err := channelMessages(ctx, api, peer, batch)
			if err != nil {
				return err
			}
			batchRefs, err := documentRefsInOrder(peer, batch, messages)
			if err != nil {
				return err
			}
			refs = append(refs, batchRefs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func channelMessages(ctx context.Context, api *tg.Client, peer tgclient.InputPeer, msgIDs []int64) (map[int64]tg.MessageClass, error) {
	ids := make([]tg.InputMessageClass, 0, len(msgIDs))
	for _, id := range msgIDs {
		ids = append(ids, &tg.InputMessageID{ID: int(id)})
	}
	result, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash},
		ID:      ids,
	})
	if err != nil {
		return nil, err
	}
	found := make(map[int64]tg.MessageClass, len(msgIDs))
	if messages, ok := result.(*tg.MessagesChannelMessages); ok {
		for _, msg := range messages.Messages {
			found[int64(msg.GetID())] = msg
		}
	}
	return found, nil
}

func documentRefsInOrder(peer tgclient.InputPeer, msgIDs []int64, messages map[int64]tg.MessageClass) ([]tgclient.DocumentRef, error) {
	refs := make([]tgclient.DocumentRef, 0, len(msgIDs))
	for _, id := range msgIDs {
		msg, ok := messages[id]
		if !ok {
			return nil, fmt.Errorf("conn: message %d: %w", id, tgclient.ErrMessageNotFound)
		}
		ref, err := documentRefFromMessage(peer, id, msg)
		if err != nil {
			return nil, fmt.Errorf("conn: message %d: %w", id, err)
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
		return tgclient.DocumentRef{}, fmt.Errorf("carries no document: %w", tgclient.ErrMessageNotFound)
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		return tgclient.DocumentRef{}, fmt.Errorf("document is empty: %w", tgclient.ErrMessageNotFound)
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
