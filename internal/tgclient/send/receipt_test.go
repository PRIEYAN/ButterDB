package send

import (
	"errors"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestExtractMsgIDHandlesTelegramSendVariants(t *testing.T) {
	tests := []struct {
		name     string
		updates  tg.UpdatesClass
		randomID int64
		want     int64
	}{
		{name: "short sent message", updates: &tg.UpdateShortSentMessage{ID: 41}, randomID: 7000, want: 41},
		{name: "random id mapping", randomID: 7001, want: 42, updates: &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 42, RandomID: 7001},
		}}},
		{name: "combined random id mapping", randomID: 7002, want: 43, updates: &tg.UpdatesCombined{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 43, RandomID: 7002},
		}}},
		{name: "other random id is ignored", randomID: 7003, want: 0, updates: &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 44, RandomID: 1},
		}}},
		{name: "new channel message", randomID: 7004, want: 45, updates: &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 45}},
		}}},
		{name: "nil updates", randomID: 7005, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := extractMsgID(test.updates, test.randomID); got != test.want {
				t.Fatalf("extractMsgID = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRequiredSendMsgIDClassifiesAcceptedResponseWithoutReceiptAsUnknown(t *testing.T) {
	msgID, err := requiredSendMsgID(&tg.Updates{}, 7003, "send control")
	if msgID != 0 || !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
		t.Fatalf("requiredSendMsgID = (%d, %v), want unknown outcome", msgID, err)
	}
}

func TestDocumentRefFromUpdatesReadsDocumentIdentity(t *testing.T) {
	updates := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 9, RandomID: 1},
		&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 9, Media: &tg.MessageMediaDocument{
			Document: &tg.Document{ID: 11, AccessHash: 12, FileReference: []byte("ref"), DCID: 4, Size: 1234},
		}}},
	}}
	ref := documentRefFromUpdates(testPeer, 9, updates)
	if ref.DocumentID != 11 || ref.AccessHash != 12 || string(ref.FileReference) != "ref" || ref.DCID != 4 || ref.Size != 1234 || ref.Peer != testPeer || ref.MsgID != 9 {
		t.Fatalf("ref = %+v", ref)
	}
}
