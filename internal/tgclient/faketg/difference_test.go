package faketg

import (
	"context"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestFakeDifferencePages(t *testing.T) {
	ctx := context.Background()

	t.Run("orders sends and deletions across pages", func(t *testing.T) {
		e := newEnv(t)
		start, _ := e.alice.GetChannelPts(ctx, e.peer)
		var ids []int64
		for i := int64(1); i <= 5; i++ {
			ids = append(ids, e.sendControl(t, e.alice, e.peer, "m", i))
		}
		if err := e.alice.DeleteMessages(ctx, e.peer, []int64{ids[1], ids[3]}); err != nil {
			t.Fatal(err)
		}

		first, err := e.alice.GetChannelDifference(ctx, e.peer, start, 3)
		if err != nil {
			t.Fatal(err)
		}
		if first.Final || first.Pts != start+3 {
			t.Fatalf("first page = %+v, want non-final at pts %d", first, start+3)
		}
		if got := messageIDs(first.Messages); !reflect.DeepEqual(got, []int64{ids[0], ids[2]}) {
			t.Fatalf("first page messages = %v, want deleted message 2 omitted", got)
		}

		second, err := e.alice.GetChannelDifference(ctx, e.peer, first.Pts, 3)
		if err != nil {
			t.Fatal(err)
		}
		if second.Final || !reflect.DeepEqual(second.DeletedMessageIDs, []int64{ids[1]}) {
			t.Fatalf("second page = %+v", second)
		}
		if got := messageIDs(second.Messages); !reflect.DeepEqual(got, []int64{ids[4]}) {
			t.Fatalf("second page messages = %v", got)
		}

		third, err := e.alice.GetChannelDifference(ctx, e.peer, second.Pts, 3)
		if err != nil {
			t.Fatal(err)
		}
		if !third.Final || !reflect.DeepEqual(third.DeletedMessageIDs, []int64{ids[3]}) || len(third.Messages) != 0 {
			t.Fatalf("third page = %+v", third)
		}
		current, _ := e.alice.GetChannelPts(ctx, e.peer)
		if third.Pts != current {
			t.Fatalf("final pts = %d, want %d", third.Pts, current)
		}
	})

	t.Run("caught up difference is final and keeps the pts", func(t *testing.T) {
		e := newEnv(t)
		e.sendControl(t, e.alice, e.peer, "m", 1)
		current, _ := e.alice.GetChannelPts(ctx, e.peer)
		diff, err := e.alice.GetChannelDifference(ctx, e.peer, current, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Final || diff.Pts != current || len(diff.Messages) != 0 || len(diff.DeletedMessageIDs) != 0 {
			t.Fatalf("difference = %+v", diff)
		}
	})

	t.Run("edit never surfaces in a difference", func(t *testing.T) {
		e := newEnv(t)
		id := e.sendControl(t, e.alice, e.peer, "original", 1)
		current, _ := e.alice.GetChannelPts(ctx, e.peer)
		if err := e.net.EditMessageText(e.peer.ChannelID, id, "tampered"); err != nil {
			t.Fatal(err)
		}
		if after, _ := e.alice.GetChannelPts(ctx, e.peer); after != current {
			t.Fatalf("pts after edit = %d, want %d", after, current)
		}
		diff, _ := e.alice.GetChannelDifference(ctx, e.peer, current, 10)
		if !diff.Final || diff.Pts != current || len(diff.Messages) != 0 || len(diff.DeletedMessageIDs) != 0 {
			t.Fatalf("difference = %+v", diff)
		}
		msgs, _ := e.alice.GetMessages(ctx, e.peer, []int64{id})
		if len(msgs) != 1 || msgs[0].Text != "tampered" {
			t.Fatalf("messages = %+v", msgs)
		}
	})

	t.Run("out of band deletion appears as deleted ids", func(t *testing.T) {
		e := newEnv(t)
		id := e.sendControl(t, e.alice, e.peer, "x", 1)
		current, _ := e.alice.GetChannelPts(ctx, e.peer)
		if _, err := e.net.DeleteOutOfBand(e.peer.ChannelID, id, 9999); err != nil {
			t.Fatal(err)
		}
		diff, _ := e.alice.GetChannelDifference(ctx, e.peer, current, 10)
		if !reflect.DeepEqual(diff.DeletedMessageIDs, []int64{id}) {
			t.Fatalf("difference = %+v", diff)
		}
	})

	t.Run("negative pts is refused", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.alice.GetChannelDifference(ctx, e.peer, -1, 10); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("seeded history is replayed from pts zero", func(t *testing.T) {
		e := newEnv(t)
		if err := e.net.SeedHistory(e.peer.ChannelID,
			tgclient.HistoryMessage{MsgID: 10, Text: "seed ten"},
			tgclient.HistoryMessage{MsgID: 4, Text: "seed four"},
		); err != nil {
			t.Fatal(err)
		}
		diff, _ := e.alice.GetChannelDifference(ctx, e.peer, 0, 100)
		if got := messageIDs(diff.Messages); !reflect.DeepEqual(got, []int64{4, 10}) || !diff.Final {
			t.Fatalf("difference = %+v", diff)
		}
		if next := e.sendControl(t, e.alice, e.peer, "after seed", 1); next != 11 {
			t.Fatalf("next id = %d, want 11", next)
		}
	})
}

func TestFakeHistoryAndMessages(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for i := int64(1); i <= 5; i++ {
		e.sendControl(t, e.alice, e.peer, "m", i)
	}

	t.Run("history is newest first and pages by offset", func(t *testing.T) {
		page1, _ := e.alice.GetHistory(ctx, e.peer, 0, 2)
		page2, _ := e.alice.GetHistory(ctx, e.peer, page1[len(page1)-1].MsgID, 2)
		if !reflect.DeepEqual(messageIDs(page1), []int64{5, 4}) || !reflect.DeepEqual(messageIDs(page2), []int64{3, 2}) {
			t.Fatalf("pages = %v %v", messageIDs(page1), messageIDs(page2))
		}
	})

	t.Run("get messages omits missing ids and collapses duplicates in request order", func(t *testing.T) {
		if err := e.alice.DeleteMessages(ctx, e.peer, []int64{2}); err != nil {
			t.Fatal(err)
		}
		got, err := e.alice.GetMessages(ctx, e.peer, []int64{3, 2, 77, 1, 3, 0, -4})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(messageIDs(got), []int64{3, 1}) {
			t.Fatalf("messages = %+v", got)
		}
	})

	t.Run("get messages keeps seeded service placeholders", func(t *testing.T) {
		e := newEnv(t)
		_ = e.net.SeedHistory(e.peer.ChannelID, tgclient.HistoryMessage{MsgID: 1, Placeholder: true})
		got, _ := e.alice.GetMessages(ctx, e.peer, []int64{1, 2})
		if len(got) != 1 || !got[0].Placeholder {
			t.Fatalf("messages = %+v", got)
		}
	})

	t.Run("history and difference limits clamp to 100", func(t *testing.T) {
		e := newEnv(t)
		for i := int64(1); i <= 130; i++ {
			e.sendControl(t, e.alice, e.peer, "m", i)
		}
		for _, limit := range []int{0, -1, 500} {
			hist, _ := e.alice.GetHistory(ctx, e.peer, 0, limit)
			diff, _ := e.alice.GetChannelDifference(ctx, e.peer, 0, limit)
			if len(hist) != 100 || len(diff.Messages) != 100 || diff.Final {
				t.Fatalf("limit %d: history %d, difference %d final %v", limit, len(hist), len(diff.Messages), diff.Final)
			}
		}
	})

	t.Run("delete splits into batches of 100 and records each", func(t *testing.T) {
		e := newEnv(t)
		var ids []int64
		for i := int64(1); i <= 230; i++ {
			ids = append(ids, e.sendControl(t, e.alice, e.peer, "m", i))
		}
		if err := e.alice.DeleteMessages(ctx, e.peer, ids); err != nil {
			t.Fatal(err)
		}
		batches := e.net.DeletedBatches(e.peer.ChannelID)
		if len(batches) != 3 || len(batches[0]) != 100 || len(batches[1]) != 100 || len(batches[2]) != 30 {
			t.Fatalf("batch sizes = %d", len(batches))
		}
		if msgs, _ := e.net.Messages(e.peer.ChannelID); len(msgs) != 0 {
			t.Fatalf("history after delete has %d messages", len(msgs))
		}
	})

	t.Run("seeded placeholders occupy history slots", func(t *testing.T) {
		e := newEnv(t)
		_ = e.net.SeedHistory(e.peer.ChannelID, tgclient.HistoryMessage{MsgID: 3, Placeholder: true}, tgclient.HistoryMessage{MsgID: 4, Text: "real"})
		got, _ := e.alice.GetHistory(ctx, e.peer, 0, 10)
		if len(got) != 2 || !got[1].Placeholder {
			t.Fatalf("history = %+v", got)
		}
	})
}

func messageIDs(msgs []tgclient.HistoryMessage) []int64 {
	var out []int64
	for _, m := range msgs {
		out = append(out, m.MsgID)
	}
	return out
}
