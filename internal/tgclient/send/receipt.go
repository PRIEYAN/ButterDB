package send

import (
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func extractMsgID(updates tg.UpdatesClass, randomID int64) int64 {
	switch u := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return int64(u.ID)
	case *tg.Updates:
		return extractMsgIDFromUpdates(u.Updates, randomID)
	case *tg.UpdatesCombined:
		return extractMsgIDFromUpdates(u.Updates, randomID)
	}
	return 0
}

func requiredSendMsgID(updates tg.UpdatesClass, randomID int64, operation string) (int64, error) {
	msgID := extractMsgID(updates, randomID)
	if msgID <= 0 {
		return 0, fmt.Errorf("send: %w: %s returned no msg id", tgclient.ErrSendOutcomeUnknown, operation)
	}
	return msgID, nil
}

func extractMsgIDFromUpdates(updates []tg.UpdateClass, randomID int64) int64 {
	for _, update := range updates {
		switch value := update.(type) {
		case *tg.UpdateMessageID:
			if value.RandomID == randomID {
				return int64(value.ID)
			}
		case *tg.UpdateNewMessage:
			if message, ok := value.Message.(*tg.Message); ok {
				return int64(message.ID)
			}
		case *tg.UpdateNewChannelMessage:
			if message, ok := value.Message.(*tg.Message); ok {
				return int64(message.ID)
			}
		}
	}
	return 0
}

func documentRefFromUpdates(peer tgclient.InputPeer, msgID int64, updates tg.UpdatesClass) tgclient.DocumentRef {
	ref := tgclient.DocumentRef{Peer: peer, MsgID: msgID}
	for _, message := range sentMessages(updates) {
		if int64(message.ID) != msgID {
			continue
		}
		media, ok := message.Media.(*tg.MessageMediaDocument)
		if !ok {
			continue
		}
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			continue
		}
		ref.Size = doc.Size
		ref.DocumentID = doc.ID
		ref.AccessHash = doc.AccessHash
		ref.FileReference = append([]byte(nil), doc.FileReference...)
		ref.DCID = doc.DCID
	}
	return ref
}

func sentMessages(updates tg.UpdatesClass) []*tg.Message {
	var list []tg.UpdateClass
	switch u := updates.(type) {
	case *tg.Updates:
		list = u.Updates
	case *tg.UpdatesCombined:
		list = u.Updates
	}
	var messages []*tg.Message
	for _, update := range list {
		switch value := update.(type) {
		case *tg.UpdateNewMessage:
			if message, ok := value.Message.(*tg.Message); ok {
				messages = append(messages, message)
			}
		case *tg.UpdateNewChannelMessage:
			if message, ok := value.Message.(*tg.Message); ok {
				messages = append(messages, message)
			}
		}
	}
	return messages
}
