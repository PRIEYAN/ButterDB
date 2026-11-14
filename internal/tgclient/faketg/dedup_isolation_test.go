package faketg

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestFakeRandomIDDedup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	t.Run("control resend returns original message", func(t *testing.T) {
		first := e.sendControl(t, e.alice, e.peer, "TDX1|t=put", 42)
		second := e.sendControl(t, e.alice, e.peer, "TDX1|t=put changed", 42)
		if first != second {
			t.Fatalf("resend id = %d, want original %d", second, first)
		}
		msgs, _ := e.net.Messages(e.peer.ChannelID)
		if len(msgs) != 1 || msgs[0].Text != "TDX1|t=put" {
			t.Fatalf("history = %+v, want the single original", msgs)
		}
	})

	t.Run("file resend returns original message and document", func(t *testing.T) {
		first := e.sendFile(t, e.alice, e.peer, "body", patternBytes(5000), 7)
		second := e.sendFile(t, e.alice, e.peer, "body", patternBytes(5000), 7)
		if first.MsgID != second.MsgID || first.Document.DocumentID != second.Document.DocumentID {
			t.Fatalf("resend = %+v, want %+v", second, first)
		}
		if got := len(e.net.SentFiles(e.peer.ChannelID)); got != 1 {
			t.Fatalf("recorded files = %d, want 1", got)
		}
	})

	t.Run("same random id in another channel is a new message", func(t *testing.T) {
		other, err := e.alice.CreateChannel(ctx, "bucket-b", "")
		if err != nil {
			t.Fatal(err)
		}
		a := e.sendControl(t, e.alice, e.peer, "x", 99)
		b := e.sendControl(t, e.alice, other, "x", 99)
		msgsB, _ := e.net.Messages(other.ChannelID)
		if len(msgsB) != 1 || msgsB[0].MsgID != b || a == 0 {
			t.Fatalf("channel b history = %+v", msgsB)
		}
	})

	t.Run("zero random id is refused", func(t *testing.T) {
		_, err := e.alice.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: e.peer, Text: "x"})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("negative random id is accepted", func(t *testing.T) {
		id := e.sendControl(t, e.alice, e.peer, "neg", -5)
		if again := e.sendControl(t, e.alice, e.peer, "neg", -5); again != id {
			t.Fatalf("resend id = %d, want %d", again, id)
		}
	})

	t.Run("body size mismatch is refused", func(t *testing.T) {
		_, err := e.alice.SendFileIdempotent(ctx, tgclient.SendFileRequest{
			Peer: e.peer, FileName: "b", Size: 10, Body: bytesReader(make([]byte, 4)), RandomID: 123,
		})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v, want ErrInvalidArgument", err)
		}
	})
}

func TestFakePerChannelIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	peerB, err := e.alice.CreateChannel(ctx, "bucket-b", "")
	if err != nil {
		t.Fatal(err)
	}
	if peerB.ChannelID == e.peer.ChannelID {
		t.Fatal("channels share an id")
	}

	idA1 := e.sendControl(t, e.alice, e.peer, "a1", 1)
	idA2 := e.sendControl(t, e.alice, e.peer, "a2", 2)
	idB1 := e.sendControl(t, e.alice, peerB, "b1", 3)
	if idA1 != 1 || idA2 != 2 || idB1 != 1 {
		t.Fatalf("message ids = %d %d %d, want 1 2 1 (per-channel sequence)", idA1, idA2, idB1)
	}

	bodyA := e.sendFile(t, e.alice, e.peer, "doc", []byte("alpha-body"), 4)
	bodyB := e.sendFile(t, e.alice, peerB, "doc", []byte("bravo-body"), 5)
	if bodyA.MsgID != bodyB.MsgID+1 {
		t.Fatalf("body ids = %d and %d, want a different per-channel sequence", bodyA.MsgID, bodyB.MsgID)
	}

	t.Run("history is per channel", func(t *testing.T) {
		histA, _ := e.alice.GetHistory(ctx, e.peer, 0, 100)
		histB, _ := e.alice.GetHistory(ctx, peerB, 0, 100)
		if len(histA) != 3 || len(histB) != 2 {
			t.Fatalf("history sizes = %d and %d, want 3 and 2", len(histA), len(histB))
		}
		for _, m := range histB {
			if m.Text == "a1" || m.Text == "a2" {
				t.Fatalf("channel b leaked %+v", m)
			}
		}
	})

	t.Run("bodies are keyed by channel and message", func(t *testing.T) {
		refA, err := e.alice.ResolveDocument(ctx, e.peer, bodyA.MsgID)
		if err != nil {
			t.Fatal(err)
		}
		refB, err := e.alice.ResolveDocument(ctx, peerB, bodyB.MsgID)
		if err != nil {
			t.Fatal(err)
		}
		bufA := make([]byte, 10)
		bufB := make([]byte, 10)
		if _, err := e.alice.ReadDocumentRange(ctx, refA, 0, bufA); err != nil {
			t.Fatal(err)
		}
		if _, err := e.alice.ReadDocumentRange(ctx, refB, 0, bufB); err != nil {
			t.Fatal(err)
		}
		if string(bufA) != "alpha-body" || string(bufB) != "bravo-body" {
			t.Fatalf("bodies = %q and %q", bufA, bufB)
		}
		if _, err := e.alice.ResolveDocument(ctx, peerB, bodyA.MsgID+5); !errors.Is(err, tgclient.ErrMessageNotFound) {
			t.Fatalf("resolve on missing message error = %v", err)
		}
	})

	t.Run("deletion is scoped to the peer channel", func(t *testing.T) {
		if err := e.alice.DeleteMessages(ctx, peerB, []int64{idA1}); err != nil {
			t.Fatal(err)
		}
		histA, _ := e.alice.GetHistory(ctx, e.peer, 0, 100)
		if len(histA) != 3 {
			t.Fatalf("channel a lost messages: %+v", histA)
		}
		histB, _ := e.alice.GetHistory(ctx, peerB, 0, 100)
		if len(histB) != 1 || histB[0].MsgID != bodyB.MsgID {
			t.Fatalf("channel b history = %+v, want only the body", histB)
		}
	})

	t.Run("pts is per channel", func(t *testing.T) {
		ptsA, _ := e.alice.GetChannelPts(ctx, e.peer)
		ptsB, _ := e.alice.GetChannelPts(ctx, peerB)
		if ptsA != 4 {
			t.Fatalf("channel a pts = %d, want 4 (1 + three messages)", ptsA)
		}
		e.sendControl(t, e.alice, e.peer, "a3", 50)
		if after, _ := e.alice.GetChannelPts(ctx, peerB); after != ptsB {
			t.Fatalf("channel b pts moved from %d to %d after a send to channel a", ptsB, after)
		}
		if after, _ := e.alice.GetChannelPts(ctx, e.peer); after != ptsA+1 {
			t.Fatalf("channel a pts = %d, want %d", after, ptsA+1)
		}
		diffA, _ := e.alice.GetChannelDifference(ctx, e.peer, 1, 100)
		if len(diffA.DeletedMessageIDs) != 0 {
			t.Fatalf("channel a saw channel b deletions: %v", diffA.DeletedMessageIDs)
		}
	})

	t.Run("a peer with another channel's hash is refused", func(t *testing.T) {
		wrong := tgclient.InputPeer{ChannelID: peerB.ChannelID, AccessHash: e.peer.AccessHash}
		if _, err := e.alice.GetHistory(ctx, wrong, 0, 10); !errors.Is(err, tgclient.ErrChannelUnavailable) {
			t.Fatalf("error = %v, want ErrChannelUnavailable", err)
		}
	})

	t.Run("zero access hash is refused", func(t *testing.T) {
		zero := tgclient.InputPeer{ChannelID: peerB.ChannelID}
		if _, err := e.alice.GetHistory(ctx, zero, 0, 10); !errors.Is(err, tgclient.ErrZeroAccessHash) {
			t.Fatalf("error = %v, want ErrZeroAccessHash", err)
		}
	})
}

func TestFakePeerValidationOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		peer tgclient.InputPeer
		want error
	}{
		{"zero hash wins over everything", tgclient.InputPeer{ChannelID: 0, AccessHash: 0}, tgclient.ErrZeroAccessHash},
		{"zero hash on a real channel", tgclient.InputPeer{ChannelID: e.peer.ChannelID}, tgclient.ErrZeroAccessHash},
		{"non positive channel id", tgclient.InputPeer{ChannelID: 0, AccessHash: 5}, tgclient.ErrChannelUnavailable},
		{"unknown channel", tgclient.InputPeer{ChannelID: 999, AccessHash: 5}, tgclient.ErrChannelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.alice.GetHistory(ctx, tc.peer, 0, 1); !errors.Is(err, tc.want) {
				t.Fatalf("GetHistory error = %v, want %v", err, tc.want)
			}
			if err := e.alice.DeleteMessages(ctx, tc.peer, []int64{1}); !errors.Is(err, tc.want) {
				t.Fatalf("DeleteMessages error = %v, want %v", err, tc.want)
			}
		})
	}
}
