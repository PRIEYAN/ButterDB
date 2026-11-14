package rpc

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type handlerInvoker func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f handlerInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

func reply(output bin.Decoder, response bin.Encoder) error {
	var buffer bin.Buffer
	if err := response.Encode(&buffer); err != nil {
		return err
	}
	return output.Decode(&buffer)
}

type fakeScope struct {
	raw handlerInvoker
}

func (s fakeScope) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	return fn(ctx, tg.NewClient(s.raw))
}

func (s fakeScope) InvokeMedia(ctx context.Context, _ int, fn func(ctx context.Context, api *tg.Client) error) error {
	return fn(ctx, tg.NewClient(s.raw))
}

type recordingGate struct {
	mu      sync.Mutex
	classes []tgclient.WriteClass
}

func (g *recordingGate) Do(_ context.Context, class tgclient.WriteClass, action func() error) error {
	g.mu.Lock()
	g.classes = append(g.classes, class)
	g.mu.Unlock()
	return action()
}

func (g *recordingGate) recorded() []tgclient.WriteClass {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]tgclient.WriteClass(nil), g.classes...)
}

func newTestClient(t *testing.T, raw handlerInvoker) (*Client, *recordingGate) {
	t.Helper()
	gate := &recordingGate{}
	client, err := New(Dependencies{Invoker: fakeScope{raw: raw}, Gate: gate, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, gate
}

var errUnexpectedCall = errors.New("unexpected rpc")

func noCalls(t *testing.T) handlerInvoker {
	return func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Error("rpc reached the wire")
		return errUnexpectedCall
	}
}

func rpcError(code int, text string) error {
	return tgerr.New(code, text)
}
