package rpc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestPlaceholderMessageReportsIDsThatOccupyHistorySlots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		msg      tg.MessageClass
		wantID   int64
		wantDate int64
		wantOK   bool
	}{
		{"service event", &tg.MessageService{ID: 1942, Date: 1787832319}, 1942, 1787832319, true},
		{"deleted message stub", &tg.MessageEmpty{ID: 2403}, 2403, 0, true},
		{"real message is not a placeholder", &tg.Message{ID: 2458}, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, date, ok := placeholderMessage(tc.msg)
			if ok != tc.wantOK || id != tc.wantID || date != tc.wantDate {
				t.Fatalf("placeholderMessage() = (%d, %d, %v), want (%d, %d, %v)", id, date, ok, tc.wantID, tc.wantDate, tc.wantOK)
			}
		})
	}
}

func TestHistoryMessageFromTG(t *testing.T) {
	document := &tg.Document{Size: 42, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "body.bin"}}}
	for _, tc := range []struct {
		name   string
		msg    tg.MessageClass
		want   tgclient.HistoryMessage
		wantOK bool
	}{
		{"text trims trailing newlines", &tg.Message{ID: 5, Date: 9, Message: "TDX1|t=put\n", FromID: &tg.PeerUser{UserID: 77}},
			tgclient.HistoryMessage{MsgID: 5, Date: 9, FromID: 77, Text: "TDX1|t=put"}, true},
		{"document body", &tg.Message{ID: 6, Media: &tg.MessageMediaDocument{Document: document}},
			tgclient.HistoryMessage{MsgID: 6, HasMedia: true, MediaSize: 42, DocumentName: "body.bin"}, true},
		{"service becomes placeholder", &tg.MessageService{ID: 7, Date: 3},
			tgclient.HistoryMessage{MsgID: 7, Date: 3, Placeholder: true}, true},
		{"non positive placeholder dropped", &tg.MessageEmpty{ID: 0}, tgclient.HistoryMessage{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := historyMessageFromTG(tc.msg)
			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("historyMessageFromTG() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestGetHistoryPagesBackwardsNewestFirst(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	for range 5 {
		channel.post("msg")
	}
	client, _ := newTestClient(t, channel.invoke)
	peer := tgclient.InputPeer{ChannelID: 12345, AccessHash: 99}

	first, err := client.GetHistory(t.Context(), peer, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || first[0].MsgID != 5 || first[2].MsgID != 3 {
		t.Fatalf("first page = %+v, want ids 5,4,3", first)
	}
	second, err := client.GetHistory(t.Context(), peer, first[2].MsgID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].MsgID != 2 || second[1].MsgID != 1 {
		t.Fatalf("second page = %+v, want ids 2,1", second)
	}
}

func TestGetHistoryReflectsDeletes(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	id := channel.post("x")
	client, _ := newTestClient(t, channel.invoke)
	peer := tgclient.InputPeer{ChannelID: 12345, AccessHash: 99}

	if err := client.DeleteMessages(t.Context(), peer, []int64{int64(id)}); err != nil {
		t.Fatal(err)
	}
	history, err := client.GetHistory(t.Context(), peer, 0, 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("history = %+v, %v; want empty", history, err)
	}
	if err := client.DeleteMessages(t.Context(), peer, []int64{int64(id), int64(id) + 999}); err != nil {
		t.Fatalf("idempotent delete of missing messages: %v", err)
	}
}

func TestGetMessagesOmitsDeletedAndKeepsRequestOrder(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	for range 4 {
		channel.post("m")
	}
	channel.remove(2)
	client, _ := newTestClient(t, channel.invoke)
	peer := tgclient.InputPeer{ChannelID: 12345, AccessHash: 99}

	got, err := client.GetMessages(t.Context(), peer, []int64{4, 2, 1, 4, 0, -3, 3})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, message := range got {
		ids = append(ids, message.MsgID)
	}
	if !reflect.DeepEqual(ids, []int64{4, 1, 3}) {
		t.Fatalf("ids = %v, want [4 1 3]", ids)
	}
}

func TestGetMessagesBatchesOf100(t *testing.T) {
	var sizes []int
	client, _ := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request := input.(*tg.ChannelsGetMessagesRequest)
		sizes = append(sizes, len(request.ID))
		return reply(output, &tg.MessagesChannelMessages{})
	})
	ids := make([]int64, 230)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := client.GetMessages(t.Context(), tgclient.InputPeer{ChannelID: 1, AccessHash: 2}, ids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sizes, []int{100, 100, 30}) {
		t.Fatalf("batch sizes = %v", sizes)
	}
}

func TestReadFloodWaitIsTyped(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	channel.floodWaits = 1
	client, _ := newTestClient(t, channel.invoke)
	peer := tgclient.InputPeer{ChannelID: 12345, AccessHash: 99}

	_, err := client.GetHistory(t.Context(), peer, 0, 10)
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || flood.Duration.Seconds() != 9 || !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("err = %v, want 9s FloodWaitError", err)
	}
	if _, err := client.GetHistory(t.Context(), peer, 0, 10); err != nil {
		t.Fatalf("after flood wait clears: %v", err)
	}
}

func TestPrivateChannelMapsToUnavailable(t *testing.T) {
	client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, _ bin.Decoder) error { return rpcError(400, "CHANNEL_PRIVATE") })
	_, err := client.GetHistory(t.Context(), tgclient.InputPeer{ChannelID: 1, AccessHash: 2}, 0, 10)
	if !errors.Is(err, tgclient.ErrChannelUnavailable) {
		t.Fatalf("err = %v, want ErrChannelUnavailable", err)
	}
}

func TestZeroAccessHashRefused(t *testing.T) {
	client, gate := newTestClient(t, noCalls(t))
	zero := tgclient.InputPeer{ChannelID: 12345}
	ctx := t.Context()

	checks := map[string]func() error{
		"get history":     func() error { _, err := client.GetHistory(ctx, zero, 0, 10); return err },
		"get messages":    func() error { _, err := client.GetMessages(ctx, zero, []int64{1}); return err },
		"delete":          func() error { return client.DeleteMessages(ctx, zero, []int64{1}) },
		"difference":      func() error { _, err := client.GetChannelDifference(ctx, zero, 1, 10); return err },
		"get channel pts": func() error { _, err := client.GetChannelPts(ctx, zero); return err },
	}
	for name, call := range checks {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, tgclient.ErrZeroAccessHash) {
				t.Fatalf("err = %v, want ErrZeroAccessHash", err)
			}
		})
	}
	if len(gate.recorded()) != 0 {
		t.Fatalf("gate used for a refused call: %v", gate.recorded())
	}
}
