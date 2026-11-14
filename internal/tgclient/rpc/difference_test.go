package rpc

import (
	"errors"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var differencePeer = tgclient.InputPeer{ChannelID: 12345, AccessHash: 99}

func TestGetChannelDifferenceOrdersEventsAfterPostAndDelete(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	client, _ := newTestClient(t, channel.invoke)
	startPts, err := client.GetChannelPts(t.Context(), differencePeer)
	if err != nil {
		t.Fatal(err)
	}
	first := channel.post("seeded")
	second := channel.post("sent")
	channel.remove(second)

	diff, err := client.GetChannelDifference(t.Context(), differencePeer, startPts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Final || diff.TooLong {
		t.Fatalf("diff flags = final %v toolong %v, want final only", diff.Final, diff.TooLong)
	}
	if len(diff.Messages) != 2 || diff.Messages[0].MsgID != int64(first) || diff.Messages[1].MsgID != int64(second) {
		t.Fatalf("messages = %+v", diff.Messages)
	}
	if !reflect.DeepEqual(diff.DeletedMessageIDs, []int64{int64(second)}) {
		t.Fatalf("deleted = %v", diff.DeletedMessageIDs)
	}
	if diff.Pts <= startPts {
		t.Fatalf("pts did not advance: %d -> %d", startPts, diff.Pts)
	}
	again, err := client.GetChannelDifference(t.Context(), differencePeer, diff.Pts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Final || len(again.Messages) != 0 || len(again.DeletedMessageIDs) != 0 || again.Pts != diff.Pts {
		t.Fatalf("expected an empty final page at the same pts, got %+v", again)
	}
}

func TestGetChannelDifferenceLimitPages(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	client, _ := newTestClient(t, channel.invoke)
	startPts, _ := client.GetChannelPts(t.Context(), differencePeer)
	for range 3 {
		channel.post("msg")
	}
	page1, err := client.GetChannelDifference(t.Context(), differencePeer, startPts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page1.Final || len(page1.Messages) != 2 {
		t.Fatalf("page1 = %+v, want 2 messages and not final", page1)
	}
	page2, err := client.GetChannelDifference(t.Context(), differencePeer, page1.Pts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page2.Final || len(page2.Messages) != 1 {
		t.Fatalf("page2 = %+v, want 1 message and final", page2)
	}
}

func TestGetChannelDifferenceTooLong(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	channel.tooLong = true
	client, _ := newTestClient(t, channel.invoke)

	diff, err := client.GetChannelDifference(t.Context(), differencePeer, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.TooLong || !diff.Final || diff.Pts != channel.pts {
		t.Fatalf("diff = %+v, want TooLong+Final at pts %d", diff, channel.pts)
	}
	again, err := client.GetChannelDifference(t.Context(), differencePeer, 1, 100)
	if err != nil || again.TooLong {
		t.Fatalf("second call = %+v, %v; too long must affect one call", again, err)
	}
	if channel.diffCalls != 2 {
		t.Fatalf("diff calls = %d, want 2", channel.diffCalls)
	}
}

func TestGetChannelPtsAdvancesAfterPost(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	client, _ := newTestClient(t, channel.invoke)
	before, err := client.GetChannelPts(t.Context(), differencePeer)
	if err != nil {
		t.Fatal(err)
	}
	channel.post("x")
	after, err := client.GetChannelPts(t.Context(), differencePeer)
	if err != nil || after <= before {
		t.Fatalf("pts before %d after %d err %v", before, after, err)
	}
	if channel.fullCalls != 2 {
		t.Fatalf("full channel calls = %d, want 2", channel.fullCalls)
	}
}

func TestGetChannelDifferenceHonoursFloodWait(t *testing.T) {
	channel := newFakeChannel(12345, 99)
	channel.floodWaits = 1
	client, _ := newTestClient(t, channel.invoke)

	if _, err := client.GetChannelDifference(t.Context(), differencePeer, 1, 100); !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("err = %v, want ErrFloodWait", err)
	}
	diff, err := client.GetChannelDifference(t.Context(), differencePeer, 1, 100)
	if err != nil || !diff.Final {
		t.Fatalf("after flood wait clears: %+v, %v", diff, err)
	}
}
