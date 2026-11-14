package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error {
	if err := requirePeer("delete messages", peer); err != nil {
		return err
	}
	for start := 0; start < len(msgIDs); start += deleteMessagesBatchSize {
		end := min(start+deleteMessagesBatchSize, len(msgIDs))
		if err := c.deleteBatch(ctx, peer, msgIDs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) deleteBatch(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error {
	ids := make([]int, 0, len(msgIDs))
	for _, id := range msgIDs {
		ids = append(ids, int(id))
	}
	return c.write(ctx, tgclient.WriteDelete, "delete messages", func(ctx context.Context, api *tg.Client) error {
		_, err := api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: inputChannel(peer), ID: ids})
		return err
	})
}

func (c *Client) SendToSavedMessages(ctx context.Context, text string) (int64, error) {
	if text == "" {
		return 0, errors.New("rpc: send to saved messages: empty text")
	}
	randomID, err := c.positiveRandomID()
	if err != nil {
		return 0, err
	}
	var msgID int64
	err = c.write(ctx, tgclient.WriteControl, "send to saved messages", func(ctx context.Context, api *tg.Client) error {
		updates, sendErr := api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:      &tg.InputPeerSelf{},
			Message:   text,
			RandomID:  randomID,
			NoWebpage: true,
		})
		if sendErr != nil {
			return mapSendError(sendErr)
		}
		msgID = sentMessageID(updates, randomID)
		if msgID <= 0 {
			return fmt.Errorf("%w: send returned no message id", tgclient.ErrSendOutcomeUnknown)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return msgID, nil
}

func mapSendError(err error) error {
	if isDefiniteRejection(err) {
		return err
	}
	return fmt.Errorf("%w: %w", tgclient.ErrSendOutcomeUnknown, err)
}

func (c *Client) positiveRandomID() (int64, error) {
	var buf [8]byte
	if _, err := c.random.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("rpc: random id: %w", err)
	}
	id := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id, nil
}

func sentMessageID(updates tg.UpdatesClass, randomID int64) int64 {
	switch value := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return int64(value.ID)
	case *tg.Updates:
		return sentMessageIDFromUpdates(value.Updates, randomID)
	case *tg.UpdatesCombined:
		return sentMessageIDFromUpdates(value.Updates, randomID)
	}
	return 0
}

func sentMessageIDFromUpdates(updates []tg.UpdateClass, randomID int64) int64 {
	var fallback int64
	for _, update := range updates {
		switch value := update.(type) {
		case *tg.UpdateMessageID:
			if value.RandomID == randomID {
				return int64(value.ID)
			}
		case *tg.UpdateNewMessage:
			if message, ok := value.Message.(*tg.Message); ok && fallback == 0 {
				fallback = int64(message.ID)
			}
		}
	}
	return fallback
}
