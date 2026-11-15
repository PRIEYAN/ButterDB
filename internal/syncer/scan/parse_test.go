package scan

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestParseHistoryPage(t *testing.T) {
	valid := bucketCreateText(t, 1)
	for _, test := range []struct {
		name    string
		msgs    []tgclient.HistoryMessage
		wantIDs []int64
	}{
		{name: "plain chat is skipped", msgs: []tgclient.HistoryMessage{{MsgID: 1, Text: "hello"}}},
		{name: "valid op kept", msgs: []tgclient.HistoryMessage{{MsgID: 2, Text: valid}}, wantIDs: []int64{2}},
		{name: "unknown op kept for the projector", msgs: []tgclient.HistoryMessage{{MsgID: 3, Text: "TDX1|t=future|j=x"}}, wantIDs: []int64{3}},
		{name: "malformed header kept for the projector", msgs: []tgclient.HistoryMessage{{MsgID: 4, Text: "TDX1|garbage"}}, wantIDs: []int64{4}},
		{name: "caption on a document", msgs: []tgclient.HistoryMessage{{MsgID: 5, Text: valid + "\nline two", HasMedia: true}}, wantIDs: []int64{5}},
		{name: "empty page", msgs: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed := ParseHistoryPage(registryID, test.msgs)
			if len(parsed) != len(test.wantIDs) {
				t.Fatalf("parsed %d messages, want %d", len(parsed), len(test.wantIDs))
			}
			for i, id := range test.wantIDs {
				if parsed[i].MsgID != id || parsed[i].Observed.MsgID != id || parsed[i].Observed.ChannelID != registryID {
					t.Fatalf("parsed[%d] = %#v, want msg %d on channel %d", i, parsed[i], id, registryID)
				}
			}
		})
	}
}

func TestParseHistoryPageCarriesObservation(t *testing.T) {
	msg := tgclient.HistoryMessage{MsgID: 9, Date: 1700000009, FromID: 77, Text: bucketCreateText(t, 1), HasMedia: true}
	parsed := ParseHistoryPage(registryID, []tgclient.HistoryMessage{msg})
	if len(parsed) != 1 {
		t.Fatalf("parsed %d messages", len(parsed))
	}
	obs := parsed[0].Observed
	if obs.MsgDate != msg.Date || obs.ActorUserID != msg.FromID || !obs.IsDocument || obs.RawText != msg.Text {
		t.Fatalf("observation = %#v, want the message fields carried over", obs)
	}
}

func TestSortAscending(t *testing.T) {
	msgs := []ParsedMessage{{MsgID: 30}, {MsgID: 10}, {MsgID: 20}}
	SortAscending(msgs)
	for i, want := range []int64{10, 20, 30} {
		if msgs[i].MsgID != want {
			t.Fatalf("order = %v, want ascending", msgs)
		}
	}
}
