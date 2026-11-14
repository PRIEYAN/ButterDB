package conn

import (
	"context"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
)

func TestChannelIDsFromUpdatesExtractsChannelDoorbells(t *testing.T) {
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: &tg.Message{PeerID: &tg.PeerChannel{ChannelID: 101}}},
			&tg.UpdateEditChannelMessage{Message: &tg.MessageService{PeerID: &tg.PeerChannel{ChannelID: 202}}},
			&tg.UpdateDeleteChannelMessages{ChannelID: 303},
			&tg.UpdateNewChannelMessage{Message: &tg.Message{PeerID: &tg.PeerChannel{ChannelID: 101}}},
		},
	}

	got := ChannelIDsFromUpdates(updates)
	want := []int64{101, 202, 303}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChannelIDsFromUpdates() = %v, want %v", got, want)
	}
}

func TestChannelIDsFromUpdatesCoversEnvelopeShapes(t *testing.T) {
	channelUpdate := &tg.UpdateDeleteChannelMessages{ChannelID: 7}
	tests := []struct {
		name    string
		updates tg.UpdatesClass
		want    []int64
	}{
		{"short", &tg.UpdateShort{Update: channelUpdate}, []int64{7}},
		{"combined", &tg.UpdatesCombined{Updates: []tg.UpdateClass{channelUpdate}}, []int64{7}},
		{"too long", &tg.UpdatesTooLong{}, nil},
		{"non positive id", &tg.UpdateShort{Update: &tg.UpdateDeleteChannelMessages{ChannelID: 0}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChannelIDsFromUpdates(tt.updates)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ChannelIDsFromUpdates() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChannelIDsFromUpdatesIgnoresNonChannelUpdates(t *testing.T) {
	got := ChannelIDsFromUpdates(&tg.UpdateShort{
		Update: &tg.UpdateNewMessage{Message: &tg.Message{PeerID: &tg.PeerUser{UserID: 10}}},
	})
	if len(got) != 0 {
		t.Fatalf("ChannelIDsFromUpdates() = %v, want empty", got)
	}
}

func TestActivityHandleNeverBlocksWhenFull(t *testing.T) {
	activity := NewActivity(1)
	activity.Signal(1)

	err := activity.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateDeleteChannelMessages{ChannelID: 2},
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if activity.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1", activity.Dropped())
	}
}
