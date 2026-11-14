package conn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func documentMessage(id int, doc tg.DocumentClass) *tg.Message {
	return &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 10}, Media: &tg.MessageMediaDocument{Document: doc}}
}

func TestDocumentRefsInOrderFollowsRequestedIDs(t *testing.T) {
	t.Parallel()

	peer := tgclient.InputPeer{ChannelID: 10, AccessHash: 20}
	messages := map[int64]tg.MessageClass{
		5: documentMessage(5, &tg.Document{ID: 50, DCID: 2, Size: 500, FileReference: []byte{5}}),
		3: documentMessage(3, &tg.Document{ID: 30, DCID: 4, Size: 300}),
	}

	refs, err := documentRefsInOrder(peer, []int64{3, 5}, messages)
	if err != nil {
		t.Fatalf("documentRefsInOrder: %v", err)
	}
	if len(refs) != 2 || refs[0].MsgID != 3 || refs[1].MsgID != 5 {
		t.Fatalf("refs = %+v, want message 3 then 5", refs)
	}
	if refs[0].DCID != 4 || refs[0].Size != 300 || refs[0].Peer != peer {
		t.Fatalf("refs[0] = %+v", refs[0])
	}
	if refs[1].DocumentID != 50 || refs[1].DCID != 2 || string(refs[1].FileReference) != "\x05" {
		t.Fatalf("refs[1] = %+v", refs[1])
	}

	_, err = documentRefsInOrder(peer, []int64{3, 4}, messages)
	if !errors.Is(err, tgclient.ErrMessageNotFound) || !strings.Contains(err.Error(), "message 4") {
		t.Fatalf("missing message err = %v, want %v naming message 4", err, tgclient.ErrMessageNotFound)
	}
}

func TestDocumentRefFromMessageClassifiesMessages(t *testing.T) {
	t.Parallel()

	peer := tgclient.InputPeer{ChannelID: 10, AccessHash: 20}
	tests := []struct {
		name    string
		message tg.MessageClass
		wantErr bool
	}{
		{"document", documentMessage(1, &tg.Document{ID: 7, DCID: 4}), false},
		{"deleted placeholder", &tg.MessageEmpty{ID: 2}, true},
		{"text only", &tg.Message{ID: 3}, true},
		{"empty document", documentMessage(4, &tg.DocumentEmpty{}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := documentRefFromMessage(peer, int64(tt.message.GetID()), tt.message)
			if tt.wantErr {
				if !errors.Is(err, tgclient.ErrMessageNotFound) {
					t.Fatalf("err = %v, want %v", err, tgclient.ErrMessageNotFound)
				}
				return
			}
			if err != nil || ref.DocumentID != 7 || ref.DCID != 4 {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})
	}
}

func TestDocumentRefFromMessageCopiesFileReference(t *testing.T) {
	t.Parallel()

	doc := &tg.Document{ID: 7, FileReference: []byte{1, 2, 3}}
	ref, err := documentRefFromMessage(tgclient.InputPeer{}, 1, documentMessage(1, doc))
	if err != nil {
		t.Fatalf("documentRefFromMessage: %v", err)
	}
	doc.FileReference[0] = 9
	if ref.FileReference[0] != 1 {
		t.Fatal("ref shares its file reference with the Telegram message")
	}
}

func TestResolveDocumentsBatchesAndKeepsOrder(t *testing.T) {
	var batches []int
	client := newFakeClient(func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request, ok := input.(*tg.ChannelsGetMessagesRequest)
		if !ok {
			t.Errorf("unexpected request %T", input)
			return errors.New("unexpected request")
		}
		batches = append(batches, len(request.ID))
		reply := &tg.MessagesChannelMessages{}
		for i := len(request.ID) - 1; i >= 0; i-- {
			id := request.ID[i].(*tg.InputMessageID).ID
			reply.Messages = append(reply.Messages, documentMessage(id, &tg.Document{ID: int64(id) * 10}))
		}
		return respondWith(output, reply)
	})
	c, _ := newTestConnection(t, client)

	ids := make([]int64, 0, 150)
	for i := int64(1); i <= 150; i++ {
		ids = append(ids, i)
	}
	refs, err := c.ResolveDocuments(context.Background(), tgclient.InputPeer{ChannelID: 10, AccessHash: 20}, ids)
	if err != nil {
		t.Fatalf("ResolveDocuments: %v", err)
	}
	if len(batches) != 2 || batches[0] != 100 || batches[1] != 50 {
		t.Fatalf("batches = %v, want [100 50]", batches)
	}
	for i, ref := range refs {
		if ref.MsgID != ids[i] || ref.DocumentID != ids[i]*10 {
			t.Fatalf("refs[%d] = %+v", i, ref)
		}
	}
}
