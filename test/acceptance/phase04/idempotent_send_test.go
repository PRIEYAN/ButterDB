package phase04

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

func mustRandomID(t *testing.T, jobID, purpose string) int64 {
	t.Helper()
	id, err := send.StableRandomID(jobID, purpose)
	if err != nil {
		t.Fatalf("StableRandomID(%q, %q): %v", jobID, purpose, err)
	}
	return id
}

func TestUncertainSendRetriesWithStableRandomID(t *testing.T) {
	t.Run("random ids are stable per job and step", func(t *testing.T) {
		first := mustRandomID(t, "job-1", "body")
		if again := mustRandomID(t, "job-1", "body"); again != first {
			t.Fatalf("same job and step gave %d then %d", first, again)
		}
		for _, other := range []struct{ job, purpose string }{{"job-1", "put"}, {"job-2", "body"}} {
			if got := mustRandomID(t, other.job, other.purpose); got == first {
				t.Fatalf("(%s, %s) reused the random id of (job-1, body)", other.job, other.purpose)
			}
		}
		if first == 0 {
			t.Fatal("random id must be non-zero")
		}
	})

	cases := []struct {
		name      string
		delivered bool
	}{
		{"accepted by telegram but receipt lost", true},
		{"never reached telegram", false},
	}
	for _, tc := range cases {
		t.Run("control retry after uncertain send: "+tc.name, func(t *testing.T) {
			w := newWorld(t)
			fault := faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: tc.delivered}
			if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, fault); err != nil {
				t.Fatalf("queue fault: %v", err)
			}
			req := tgclient.SendControlRequest{Peer: w.peer, Text: "TDX1|t=put|j=job-1", RandomID: mustRandomID(t, "job-1", "put")}
			if _, err := w.client.SendControlIdempotent(context.Background(), req); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
				t.Fatalf("first send = %v, want ErrSendOutcomeUnknown", err)
			}
			msgID, err := w.client.SendControlIdempotent(context.Background(), req)
			if err != nil || msgID == 0 {
				t.Fatalf("retry = %d, %v; want a message id", msgID, err)
			}
			if again, err := w.client.SendControlIdempotent(context.Background(), req); err != nil || again != msgID {
				t.Fatalf("second retry = %d, %v; want the same message %d", again, err, msgID)
			}
			if sent := w.network.SentControls(channelID); len(sent) != 1 || sent[0].MsgID != msgID {
				t.Fatalf("channel holds %d control messages, want exactly the one retried message %d", len(sent), msgID)
			}
		})
	}

	t.Run("document retry rewinds the body and lands once", func(t *testing.T) {
		w := newWorld(t)
		if err := w.client.QueueFaults(faketg.MethodSendFileIdempotent, faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		body, data := newBody(300_000)
		req := tgclient.SendFileRequest{
			Peer: w.peer, Caption: "TDX1|t=body|j=job-1", FileName: "body.bin", Size: int64(len(data)),
			Body: body, RandomID: mustRandomID(t, "job-1", "body"),
		}
		if _, err := w.client.SendFileIdempotent(context.Background(), req); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("first send = %v, want ErrSendOutcomeUnknown", err)
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		result, err := w.client.SendFileIdempotent(context.Background(), req)
		if err != nil || result.MsgID == 0 || result.Document.Size != int64(len(data)) {
			t.Fatalf("retry = %+v, %v", result, err)
		}
		if sent := w.network.SentFiles(channelID); len(sent) != 1 || sent[0].MsgID != result.MsgID {
			t.Fatalf("channel holds %d documents, want exactly 1", len(sent))
		}
	})

	t.Run("accepted with an error is also deduplicated on retry", func(t *testing.T) {
		w := newWorld(t)
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultAcceptedWithError}); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		req := tgclient.SendControlRequest{Peer: w.peer, Text: "TDX1|t=del", RandomID: mustRandomID(t, "job-2", "tombstone")}
		if _, err := w.client.SendControlIdempotent(context.Background(), req); !errors.Is(err, faketg.ErrInjectedAccepted) {
			t.Fatalf("first send = %v, want the accepted-with-error failure", err)
		}
		if _, err := w.client.SendControlIdempotent(context.Background(), req); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if sent := w.network.SentControls(channelID); len(sent) != 1 {
			t.Fatalf("channel holds %d control messages, want 1", len(sent))
		}
	})

	t.Run("a fresh random id on retry would duplicate", func(t *testing.T) {
		w := newWorld(t)
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		first := tgclient.SendControlRequest{Peer: w.peer, Text: "TDX1|t=put", RandomID: 1001}
		if _, err := w.client.SendControlIdempotent(context.Background(), first); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("first send = %v", err)
		}
		retry := first
		retry.RandomID = 1003
		if _, err := w.client.SendControlIdempotent(context.Background(), retry); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if sent := w.network.SentControls(channelID); len(sent) != 2 {
			t.Fatalf("channel holds %d messages; the contrast case must show the duplicate that a stable id prevents", len(sent))
		}
	})
}
