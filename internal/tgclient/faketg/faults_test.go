package faketg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestFakeScriptedFaults(t *testing.T) {
	ctx := context.Background()
	control := func(e env, randomID int64) (int64, error) {
		return e.alice.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: e.peer, Text: "op", RandomID: randomID})
	}

	t.Run("flood wait is typed and the next call succeeds", func(t *testing.T) {
		e := newEnv(t)
		if err := e.alice.QueueFaults(MethodSendControlIdempotent, Fault{Kind: FaultFloodWait, Wait: 7 * time.Second}); err != nil {
			t.Fatal(err)
		}
		_, err := control(e, 1)
		var flood *tgclient.FloodWaitError
		if !errors.As(err, &flood) || flood.Duration != 7*time.Second || !errors.Is(err, tgclient.ErrFloodWait) {
			t.Fatalf("error = %v, want 7s FloodWaitError", err)
		}
		if msgs, _ := e.net.Messages(e.peer.ChannelID); len(msgs) != 0 {
			t.Fatalf("flood waited send was delivered: %+v", msgs)
		}
		if _, err := control(e, 1); err != nil {
			t.Fatalf("retry error = %v", err)
		}
	})

	t.Run("premium flood wait is a flood wait", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendControlIdempotent, Fault{Kind: FaultFloodWait, Wait: 90 * time.Second, Premium: true})
		_, err := control(e, 1)
		var flood *tgclient.FloodWaitError
		if !errors.As(err, &flood) || flood.Duration != 90*time.Second || !strings.Contains(err.Error(), "FLOOD_PREMIUM_WAIT_90") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("flood wait on a read method with the nth call script", func(t *testing.T) {
		e := newEnv(t)
		if err := e.alice.FaultOnCall(MethodGetHistory, 2, Fault{Kind: FaultFloodWait, Wait: 3 * time.Second}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.alice.GetHistory(ctx, e.peer, 0, 10); err != nil {
			t.Fatalf("first call: %v", err)
		}
		if _, err := e.alice.GetHistory(ctx, e.peer, 0, 10); !errors.Is(err, tgclient.ErrFloodWait) {
			t.Fatalf("second call error = %v", err)
		}
		if _, err := e.alice.GetHistory(ctx, e.peer, 0, 10); err != nil {
			t.Fatalf("third call: %v", err)
		}
		if got := e.alice.CallCount(MethodGetHistory); got != 3 {
			t.Fatalf("call count = %d", got)
		}
	})

	t.Run("accepted with error returns the message id and an error", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendControlIdempotent, Fault{Kind: FaultAcceptedWithError})
		id, err := control(e, 5)
		if id == 0 || !errors.Is(err, ErrInjectedAccepted) {
			t.Fatalf("result = %d, %v", id, err)
		}
		msgs, _ := e.net.Messages(e.peer.ChannelID)
		if len(msgs) != 1 || msgs[0].MsgID != id {
			t.Fatalf("message not delivered: %+v", msgs)
		}
		if again, err := control(e, 5); err != nil || again != id {
			t.Fatalf("resend = %d, %v, want original %d", again, err, id)
		}
	})

	t.Run("accepted with error on a file send carries the document", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendFileIdempotent, Fault{Kind: FaultAcceptedWithError})
		res, err := e.alice.SendFileIdempotent(ctx, tgclient.SendFileRequest{
			Peer: e.peer, FileName: "b", Size: 3, Body: bytesReader([]byte("abc")), RandomID: 9,
		})
		if res.MsgID == 0 || res.Document.DocumentID == 0 || !errors.Is(err, ErrInjectedAccepted) {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})

	t.Run("outcome unknown with the message delivered", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendControlIdempotent, Fault{Kind: FaultOutcomeUnknown, Delivered: true})
		id, err := control(e, 5)
		if id != 0 || !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("result = %d, %v", id, err)
		}
		if msgs, _ := e.net.Messages(e.peer.ChannelID); len(msgs) != 1 {
			t.Fatalf("history = %+v, want the delivered message", msgs)
		}
		resent, err := control(e, 5)
		if err != nil || resent != 1 {
			t.Fatalf("resend = %d, %v, want original id 1", resent, err)
		}
	})

	t.Run("outcome unknown with the message not delivered", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendFileIdempotent, Fault{Kind: FaultOutcomeUnknown})
		_, err := e.alice.SendFileIdempotent(ctx, tgclient.SendFileRequest{
			Peer: e.peer, FileName: "b", Size: 3, Body: bytesReader([]byte("abc")), RandomID: 9,
		})
		if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("error = %v", err)
		}
		if msgs, _ := e.net.Messages(e.peer.ChannelID); len(msgs) != 0 {
			t.Fatalf("history = %+v, want empty", msgs)
		}
		res, err := e.alice.SendFileIdempotent(ctx, tgclient.SendFileRequest{
			Peer: e.peer, FileName: "b", Size: 3, Body: bytesReader([]byte("abc")), RandomID: 9,
		})
		if err != nil || res.MsgID != 1 {
			t.Fatalf("resend = %+v, %v", res, err)
		}
	})

	t.Run("transport drop delivers nothing", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodSendControlIdempotent, Fault{Kind: FaultTransportDrop})
		_, err := control(e, 5)
		if !errors.Is(err, ErrInjectedTransport) || !strings.Contains(err.Error(), "engine forcibly closed") {
			t.Fatalf("error = %v", err)
		}
		if msgs, _ := e.net.Messages(e.peer.ChannelID); len(msgs) != 0 {
			t.Fatalf("history = %+v", msgs)
		}
	})

	t.Run("expired file reference fails once then succeeds after re-resolve", func(t *testing.T) {
		e := newEnv(t)
		body := patternBytes(8192)
		sent := e.sendFile(t, e.alice, e.peer, "doc", body, 3)
		stale := sent.Document
		_ = e.alice.QueueFaults(MethodReadDocumentRange, Fault{Kind: FaultExpireFileReference})
		buf := make([]byte, 4096)
		if _, err := e.alice.ReadDocumentRange(ctx, stale, 0, buf); !errors.Is(err, tgclient.ErrFileReferenceExpired) {
			t.Fatalf("first read error = %v", err)
		}
		if _, err := e.alice.ReadDocumentRange(ctx, stale, 0, buf); !errors.Is(err, tgclient.ErrFileReferenceExpired) {
			t.Fatalf("read with the stale reference error = %v", err)
		}
		fresh, err := e.alice.ResolveDocument(ctx, e.peer, sent.MsgID)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(fresh.FileReference, stale.FileReference) {
			t.Fatal("re-resolve returned the same file reference")
		}
		if n, err := e.alice.ReadDocumentRange(ctx, fresh, 4096, buf); err != nil || n != 4096 || string(buf) != string(body[4096:]) {
			t.Fatalf("read after refresh = %d, %v", n, err)
		}
	})

	t.Run("cdn redirect is served and counted", func(t *testing.T) {
		e := newEnv(t)
		body := patternBytes(4096)
		sent := e.sendFile(t, e.alice, e.peer, "doc", body, 3)
		_ = e.alice.QueueFaults(MethodReadDocumentRange, Fault{Kind: FaultCDNRedirect})
		buf := make([]byte, 4096)
		if n, err := e.alice.ReadDocumentRange(ctx, sent.Document, 0, buf); err != nil || n != 4096 || e.alice.CDNRedirectCount() != 1 {
			t.Fatalf("read = %d, %v, redirects %d", n, err, e.alice.CDNRedirectCount())
		}
		hashErr := errors.New("cdn hash mismatch")
		_ = e.alice.QueueFaults(MethodReadDocumentRange, Fault{Kind: FaultCDNRedirect, Err: hashErr})
		if _, err := e.alice.ReadDocumentRange(ctx, sent.Document, 0, buf); !errors.Is(err, hashErr) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("latency advances the injected clock without sleeping", func(t *testing.T) {
		e := newEnv(t)
		before := e.clock.Now()
		_ = e.alice.QueueFaults(MethodSendFileIdempotent, Fault{Kind: FaultLatency, Latency: 40 * time.Second})
		e.sendFile(t, e.alice, e.peer, "doc", []byte("x"), 1)
		if elapsed := e.clock.Now().Sub(before); elapsed != 40*time.Second {
			t.Fatalf("clock advanced %v, want 40s", elapsed)
		}
		if err := e.alice.SetLatency(MethodGetHistory, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		before = e.clock.Now()
		_, _ = e.alice.GetHistory(ctx, e.peer, 0, 10)
		_, _ = e.alice.GetHistory(ctx, e.peer, 0, 10)
		if elapsed := e.clock.Now().Sub(before); elapsed != 4*time.Second {
			t.Fatalf("clock advanced %v, want 4s", elapsed)
		}
	})

	t.Run("latency honours a cancelled context", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.SetLatency(MethodGetHistory, time.Second)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := e.alice.GetHistory(cancelled, e.peer, 0, 10); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("scripted deletion removes messages before the call", func(t *testing.T) {
		e := newEnv(t)
		id := e.sendControl(t, e.alice, e.peer, "x", 1)
		_ = e.alice.QueueFaults(MethodGetMessages, Fault{Kind: FaultDeleteMessages, MessageIDs: []int64{id}})
		got, err := e.alice.GetMessages(ctx, e.peer, []int64{id})
		if err != nil || len(got) != 0 {
			t.Fatalf("messages = %+v, %v", got, err)
		}
		if _, err := e.alice.ResolveDocument(ctx, e.peer, id); !errors.Is(err, tgclient.ErrMessageNotFound) {
			t.Fatalf("resolve error = %v", err)
		}
	})

	t.Run("partial delete removes a prefix then fails", func(t *testing.T) {
		e := newEnv(t)
		var ids []int64
		for i := int64(1); i <= 5; i++ {
			ids = append(ids, e.sendControl(t, e.alice, e.peer, "x", i))
		}
		_ = e.alice.QueueFaults(MethodDeleteMessages, Fault{Kind: FaultPartialDelete, Count: 2})
		if err := e.alice.DeleteMessages(ctx, e.peer, ids); !errors.Is(err, ErrInjectedTransport) {
			t.Fatalf("error = %v", err)
		}
		left, _ := e.net.Messages(e.peer.ChannelID)
		if !reflect.DeepEqual(messageIDs(left), ids[2:]) {
			t.Fatalf("remaining = %v, want %v", messageIDs(left), ids[2:])
		}
		if err := e.alice.DeleteMessages(ctx, e.peer, ids); err != nil {
			t.Fatalf("retry error = %v", err)
		}
	})

	t.Run("outcome unknown on create channel and saved messages", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodCreateChannel, Fault{Kind: FaultOutcomeUnknown, Delivered: true})
		peer, err := e.alice.CreateChannel(ctx, "ghost", "")
		if peer != (tgclient.InputPeer{}) || !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("CreateChannel = %+v, %v", peer, err)
		}
		owned, _ := e.alice.ListOwnedChannels(ctx)
		if len(owned) != 2 || owned[1].Title != "ghost" {
			t.Fatalf("owned = %+v, want the created channel to exist", owned)
		}
		_ = e.alice.QueueFaults(MethodSendToSavedMessages, Fault{Kind: FaultOutcomeUnknown})
		id, err := e.alice.SendToSavedMessages(ctx, "link")
		if id != 0 || !errors.Is(err, tgclient.ErrSendOutcomeUnknown) || len(e.alice.SavedMessages()) != 0 {
			t.Fatalf("SendToSavedMessages = %d, %v", id, err)
		}
	})

	t.Run("difference too long", func(t *testing.T) {
		e := newEnv(t)
		_ = e.alice.QueueFaults(MethodGetChannelDifference, Fault{Kind: FaultDifferenceTooLong})
		diff, err := e.alice.GetChannelDifference(ctx, e.peer, 1, 10)
		if err != nil || !diff.TooLong || !diff.Final {
			t.Fatalf("difference = %+v, %v", diff, err)
		}
	})

	t.Run("custom error fault on an auth method", func(t *testing.T) {
		e := newEnv(t)
		boom := errors.New("hint unavailable")
		_ = e.alice.QueueFaults(MethodGetPasswordHint, Fault{Kind: FaultError, Err: boom})
		if _, err := e.alice.GetPasswordHint(ctx); !errors.Is(err, boom) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid scripts are rejected", func(t *testing.T) {
		e := newEnv(t)
		for name, tc := range map[string]struct {
			method Method
			fault  Fault
		}{
			"unknown method":          {"Nope", Fault{Kind: FaultTransportDrop}},
			"flood wait without wait": {MethodGetHistory, Fault{Kind: FaultFloodWait}},
			"expire on a send":        {MethodSendControlIdempotent, Fault{Kind: FaultExpireFileReference}},
			"unknown on a read":       {MethodGetHistory, Fault{Kind: FaultOutcomeUnknown}},
			"error without error":     {MethodGetHistory, Fault{Kind: FaultError}},
			"delete without ids":      {MethodGetHistory, Fault{Kind: FaultDeleteMessages}},
		} {
			if err := e.alice.QueueFaults(tc.method, tc.fault); !errors.Is(err, ErrInvalidFaultScript) {
				t.Errorf("%s: error = %v", name, err)
			}
		}
	})
}

func TestFakeConcurrentUse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for worker := int64(0); worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int64(1); i <= 25; i++ {
				_, err := e.alice.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: e.peer, Text: "c", RandomID: worker*1000 + i})
				if err != nil {
					t.Errorf("send: %v", err)
					return
				}
				_, _ = e.alice.GetHistory(ctx, e.peer, 0, 10)
				_, _ = e.alice.GetChannelDifference(ctx, e.peer, 0, 10)
			}
		}()
	}
	wg.Wait()
	msgs, _ := e.net.Messages(e.peer.ChannelID)
	if len(msgs) != 200 {
		t.Fatalf("messages = %d, want 200", len(msgs))
	}
	for i, m := range msgs {
		if m.MsgID != int64(i+1) {
			t.Fatalf("message %d has id %d, ids must be a gapless sequence", i, m.MsgID)
		}
	}
}

func TestFakeRangePreconditions(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	body := patternBytes(3*1024*1024 + 100)
	sent := e.sendFile(t, e.alice, e.peer, "big", body, 1)
	for _, tc := range []struct {
		name    string
		offset  int64
		length  int
		wantErr error
		wantN   int
	}{
		{"aligned block", 4096, 4096, nil, 4096},
		{"unaligned offset", 100, 4096, errs.ErrInvalidRange, 0},
		{"over one mebibyte", 0, 1024*1024 + 1, errs.ErrInvalidRange, 0},
		{"crosses a mebibyte boundary", 1024*1024 - 4096, 8192, errs.ErrInvalidRange, 0},
		{"final short block", 3 * 1024 * 1024, 100, nil, 100},
		{"past the end", 3 * 1024 * 1024, 4096, errUnexpectedEOF, 0},
		{"negative offset", -4096, 10, errs.ErrInvalidRange, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.length)
			n, err := e.alice.ReadDocumentRange(ctx, sent.Document, tc.offset, buf)
			if !errors.Is(err, tc.wantErr) || n != tc.wantN {
				t.Fatalf("read = %d, %v, want %d, %v", n, err, tc.wantN, tc.wantErr)
			}
			if err == nil && string(buf[:n]) != string(body[tc.offset:tc.offset+int64(n)]) {
				t.Fatal("bytes differ")
			}
		})
	}
}
