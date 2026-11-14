package send

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	valid := func() Dependencies {
		return Dependencies{
			Invoker:      newAPIInvoker(nil),
			Writes:       &recordingGate{},
			Slots:        newSlotPool(1),
			UploadRetry:  newScriptedRetry(),
			ControlRetry: newScriptedRetry(),
			Logger:       discardLogger(),
		}
	}
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{name: "invoker", mutate: func(d *Dependencies) { d.Invoker = nil }},
		{name: "writes", mutate: func(d *Dependencies) { d.Writes = nil }},
		{name: "slots", mutate: func(d *Dependencies) { d.Slots = nil }},
		{name: "upload retry", mutate: func(d *Dependencies) { d.UploadRetry = nil }},
		{name: "control retry", mutate: func(d *Dependencies) { d.ControlRetry = nil }},
		{name: "logger", mutate: func(d *Dependencies) { d.Logger = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := valid()
			test.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := New(valid()); err != nil {
		t.Fatalf("New with every dependency: %v", err)
	}
}

func TestSendControlIdempotentReturnsOriginalMessage(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	randomID, err := StableRandomID("op-control", "put")
	if err != nil {
		t.Fatalf("StableRandomID: %v", err)
	}
	first, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "one", RandomID: randomID})
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	second, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "retry payload", RandomID: randomID})
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if second != first || telegram.sentCount() != 1 {
		t.Fatalf("retry id = %d (original %d), sends = %d; want one send", second, first, telegram.sentCount())
	}
}

func TestSendControlUsesSilentPlainText(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "TDX1|t=put", RandomID: 5})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	request := telegram.sent[0].(*tg.MessagesSendMessageRequest)
	peer, ok := request.Peer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != testPeer.ChannelID || peer.AccessHash != testPeer.AccessHash {
		t.Fatalf("peer = %+v", request.Peer)
	}
	if !request.Silent || !request.NoWebpage || request.Message != "TDX1|t=put" || request.RandomID != 5 {
		t.Fatalf("request = %+v, want silent no-webpage plain text with the random id", request)
	}
	if len(request.Entities) != 0 {
		t.Fatalf("entities = %v, want none", request.Entities)
	}
	if got := h.gate.recorded(); len(got) != 1 || got[0] != tgclient.WriteControl {
		t.Fatalf("write classes = %v, want one WriteControl", got)
	}
}

func TestSendFileUsesForceFileOctetStreamSilent(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	body := strings.NewReader("hello body")
	result, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{
		Peer: testPeer, Caption: "TDX1|t=body", FileName: "job.bin", Size: body.Size(), Body: body, RandomID: 9,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.MsgID == 0 || result.Document.MsgID != result.MsgID || result.Document.Peer != testPeer {
		t.Fatalf("result = %+v", result)
	}
	request := telegram.sent[0].(*tg.MessagesSendMediaRequest)
	media := request.Media.(*tg.InputMediaUploadedDocument)
	if !media.ForceFile || media.MimeType != "application/octet-stream" || !request.Silent {
		t.Fatalf("media = %+v silent=%v", media, request.Silent)
	}
	if _, hasThumb := media.GetThumb(); hasThumb {
		t.Fatal("body must not carry a thumbnail")
	}
	if len(media.Attributes) != 1 || media.Attributes[0].(*tg.DocumentAttributeFilename).FileName != "job.bin" {
		t.Fatalf("attributes = %+v", media.Attributes)
	}
	if request.Message != "TDX1|t=body" {
		t.Fatalf("caption = %q", request.Message)
	}
	if got := h.gate.recorded(); len(got) != 1 || got[0] != tgclient.WriteDocument {
		t.Fatalf("write classes = %v, want one WriteDocument", got)
	}
}

func TestSendFileIdempotentDoesNotCreateDuplicate(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	send := func(content string) tgclient.SendFileResult {
		body := strings.NewReader(content)
		result, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{
			Peer: testPeer, FileName: "job.bin", Size: body.Size(), Body: body, RandomID: 77,
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		return result
	}
	first := send("hello")
	second := send("other")
	if first.MsgID != second.MsgID || telegram.sentCount() != 1 {
		t.Fatalf("ids %d and %d with %d sends, want one send", first.MsgID, second.MsgID, telegram.sentCount())
	}
}

func TestSendFileRewindsBodyBeforeUpload(t *testing.T) {
	telegram := newFakeTelegram()
	var uploaded []byte
	h := newHarness(t, func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		if part, ok := input.(*tg.UploadSaveFilePartRequest); ok {
			uploaded = append(uploaded, part.Bytes...)
		}
		return telegram.handle(ctx, input, output)
	})
	body := bytes.NewReader([]byte("rewind me please"))
	if _, err := body.Seek(7, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	_, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{
		Peer: testPeer, FileName: "job.bin", Size: body.Size(), Body: body, RandomID: 3,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if string(uploaded) != "rewind me please" {
		t.Fatalf("uploaded %q, want the whole body from offset zero", uploaded)
	}
}

func TestSendRejectsInvalidRequestsBeforeTheWire(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	body := strings.NewReader("x")
	tests := []struct {
		name string
		call func() error
		want error
	}{
		{name: "control zero random id", want: ErrInvalidRandomID, call: func() error {
			_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "x"})
			return err
		}},
		{name: "control zero access hash", want: tgclient.ErrZeroAccessHash, call: func() error {
			_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: tgclient.InputPeer{ChannelID: 1}, Text: "x", RandomID: 1})
			return err
		}},
		{name: "control empty text", want: ErrInvalidRequest, call: func() error {
			_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, RandomID: 1})
			return err
		}},
		{name: "file zero random id", want: ErrInvalidRandomID, call: func() error {
			_, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{Peer: testPeer, FileName: "a", Size: 1, Body: body})
			return err
		}},
		{name: "file zero access hash", want: tgclient.ErrZeroAccessHash, call: func() error {
			_, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{FileName: "a", Size: 1, Body: body, RandomID: 1})
			return err
		}},
		{name: "file nil body", want: ErrInvalidRequest, call: func() error {
			_, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{Peer: testPeer, FileName: "a", Size: 1, RandomID: 1})
			return err
		}},
		{name: "file zero size", want: ErrInvalidRequest, call: func() error {
			_, err := h.sender.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{Peer: testPeer, FileName: "a", Body: body, RandomID: 1})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if telegram.sentCount() != 0 || h.invoker.scopes != 0 {
		t.Fatalf("a rejected request reached the wire: sends=%d scopes=%d", telegram.sentCount(), h.invoker.scopes)
	}
}

type failAfterInvoker struct {
	*apiInvoker
	err error
}

func (f failAfterInvoker) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	if err := f.apiInvoker.Invoke(ctx, fn); err != nil {
		return err
	}
	return f.err
}

func TestAcceptedWithErrorIsAccepted(t *testing.T) {
	telegram := newFakeTelegram()
	h := newHarness(t, telegram.handle)
	h.sender.invoker = failAfterInvoker{apiInvoker: h.invoker, err: errors.New("connection reset by peer after receipt")}
	msgID, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "x", RandomID: 4})
	if err != nil || msgID == 0 {
		t.Fatalf("send = (%d, %v), want accepted with the receipt id", msgID, err)
	}
	if telegram.sentCount() != 1 || len(h.retry.slept) != 0 {
		t.Fatalf("sends = %d, want exactly one and no retry", telegram.sentCount())
	}
}

func TestSendOutcomeUnknownKeepsItsType(t *testing.T) {
	transport := errors.New("rpc: server closed the connection")
	h := newHarness(t, func(context.Context, bin.Encoder, bin.Decoder) error { return transport })
	_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "x", RandomID: 4})
	if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) || !errors.Is(err, transport) {
		t.Fatalf("error = %v, want unknown outcome wrapping the transport error", err)
	}
}

func TestFloodWaitIsDefiniteAndTyped(t *testing.T) {
	calls := 0
	h := newHarness(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return tgerr.New(420, "FLOOD_PREMIUM_WAIT_7")
	})
	h.retry.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	_, err := h.sender.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: testPeer, Text: "x", RandomID: 4})
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || flood.Duration != 7*time.Second {
		t.Fatalf("error = %v, want a 7s FloodWaitError", err)
	}
	if errors.Is(err, tgclient.ErrSendOutcomeUnknown) || calls != 1 {
		t.Fatalf("flood wait classified as unknown or retried in place: err=%v calls=%d", err, calls)
	}
}
