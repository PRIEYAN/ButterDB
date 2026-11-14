package faketg

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type env struct {
	clock *clock.Fake
	net   *Network
	alice *Client
	peer  tgclient.InputPeer
}

func newEnv(t *testing.T) env {
	t.Helper()
	clk := clock.NewFake(time.Unix(1763000000, 0).UTC())
	n, err := NewNetwork(clk)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	alice, err := n.AddAccount(AccountConfig{UserID: 11, FirstName: "Alice", Username: "alice"})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	peer, err := alice.CreateChannel(context.Background(), "bucket-a", "about a")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	return env{clock: clk, net: n, alice: alice, peer: peer}
}

func (e env) sendControl(t *testing.T, c *Client, peer tgclient.InputPeer, text string, randomID int64) int64 {
	t.Helper()
	id, err := c.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: peer, Text: text, RandomID: randomID})
	if err != nil {
		t.Fatalf("SendControlIdempotent(%q): %v", text, err)
	}
	return id
}

func (e env) sendFile(t *testing.T, c *Client, peer tgclient.InputPeer, name string, body []byte, randomID int64) tgclient.SendFileResult {
	t.Helper()
	res, err := c.SendFileIdempotent(context.Background(), tgclient.SendFileRequest{
		Peer: peer, Caption: "TDX1|t=body", FileName: name, Size: int64(len(body)), Body: bytes.NewReader(body), RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("SendFileIdempotent(%q): %v", name, err)
	}
	return res
}

func patternBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + i/251)
	}
	return out
}

func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}

var errUnexpectedEOF = io.ErrUnexpectedEOF
