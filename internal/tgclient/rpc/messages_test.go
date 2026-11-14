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

func TestDeleteMessagesBatchesOf100(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	client, gate := newTestClient(t, channel.invoke)
	ids := make([]int64, 250)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if err := client.DeleteMessages(t.Context(), differencePeer, ids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channel.deleteSizes, []int{100, 100, 50}) {
		t.Fatalf("batch sizes = %v, want [100 100 50]", channel.deleteSizes)
	}
	classes := gate.recorded()
	if len(classes) != 3 {
		t.Fatalf("gate calls = %v", classes)
	}
	for _, class := range classes {
		if class != tgclient.WriteDelete {
			t.Fatalf("gate class = %v, want WriteDelete", class)
		}
	}
}

func TestDeleteMessagesEmptyMakesNoCall(t *testing.T) {
	client, gate := newTestClient(t, noCalls(t))
	if err := client.DeleteMessages(t.Context(), differencePeer, nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.recorded()) != 0 {
		t.Fatalf("gate used for an empty delete: %v", gate.recorded())
	}
}

func TestDeleteMessagesStopsAtFailedBatch(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		calls++
		if calls == 2 {
			return rpcError(420, "FLOOD_WAIT_30")
		}
		return reply(output, &tg.MessagesAffectedMessages{})
	})
	ids := make([]int64, 250)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	err := client.DeleteMessages(t.Context(), differencePeer, ids)
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || calls != 2 {
		t.Fatalf("err = %v after %d calls, want flood wait after 2", err, calls)
	}
}

func TestSavedMessagesNoPreview(t *testing.T) {
	var request *tg.MessagesSendMessageRequest
	client, gate := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request = input.(*tg.MessagesSendMessageRequest)
		return reply(output, &tg.UpdateShortSentMessage{ID: 321})
	})
	id, err := client.SendToSavedMessages(t.Context(), "https://example.invalid/setup")
	if err != nil || id != 321 {
		t.Fatalf("id = %d, err = %v", id, err)
	}
	if _, self := request.Peer.(*tg.InputPeerSelf); !self {
		t.Fatalf("peer = %T, want inputPeerSelf", request.Peer)
	}
	if !request.NoWebpage {
		t.Fatal("link preview not disabled")
	}
	if request.RandomID <= 0 || request.Message != "https://example.invalid/setup" {
		t.Fatalf("request = %+v", request)
	}
	if !reflect.DeepEqual(gate.recorded(), []tgclient.WriteClass{tgclient.WriteControl}) {
		t.Fatalf("gate classes = %v", gate.recorded())
	}
}

func TestSavedMessagesIDFromUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updates tg.UpdatesClass
		want    int64
		wantErr bool
	}{
		{"message id for another random id is unknown", &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 9, RandomID: 1}, &tg.UpdateMessageID{ID: 10, RandomID: 0},
		}}, 0, true},
		{"new message fallback", &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: &tg.Message{ID: 11, PeerID: &tg.PeerUser{UserID: 1}}},
		}}, 11, false},
		{"no receipt is unknown", &tg.Updates{}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
				return reply(output, tc.updates)
			})
			id, err := client.SendToSavedMessages(t.Context(), "hi")
			if tc.wantErr {
				if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
					t.Fatalf("err = %v, want ErrSendOutcomeUnknown", err)
				}
				return
			}
			if err != nil || id != tc.want {
				t.Fatalf("id = %d, err = %v, want %d", id, err, tc.want)
			}
		})
	}
}

func TestSavedMessagesErrorClassification(t *testing.T) {
	transport := errors.New("connection reset")
	for _, tc := range []struct {
		name        string
		failure     error
		wantUnknown bool
		wantFlood   bool
	}{
		{"flood wait is definite", rpcError(420, "FLOOD_WAIT_5"), false, true},
		{"rpc rejection is definite", rpcError(400, "MESSAGE_EMPTY"), false, false},
		{"transport failure is unknown", transport, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestClient(t, func(context.Context, bin.Encoder, bin.Decoder) error { return tc.failure })
			_, err := client.SendToSavedMessages(t.Context(), "hi")
			var flood *tgclient.FloodWaitError
			if errors.Is(err, tgclient.ErrSendOutcomeUnknown) != tc.wantUnknown || errors.As(err, &flood) != tc.wantFlood {
				t.Fatalf("err = %v, want unknown=%v flood=%v", err, tc.wantUnknown, tc.wantFlood)
			}
		})
	}
}

func TestSavedMessagesRejectsEmptyText(t *testing.T) {
	client, _ := newTestClient(t, noCalls(t))
	if _, err := client.SendToSavedMessages(t.Context(), ""); err == nil {
		t.Fatal("empty text accepted")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	gate := &recordingGate{}
	for name, deps := range map[string]Dependencies{
		"invoker": {Gate: gate, Random: zeroReader{}},
		"gate":    {Invoker: fakeScope{}, Random: zeroReader{}},
		"random":  {Invoker: fakeScope{}, Gate: gate},
	} {
		if _, err := New(deps); err == nil {
			t.Errorf("%s: New accepted missing dependency", name)
		}
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }
