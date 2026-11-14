package phase04

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

const (
	userID    = int64(777000123)
	channelID = int64(2000)
	baseTime  = int64(1763000000)
)

type world struct {
	clk     *clock.Fake
	network *faketg.Network
	client  *faketg.Client
	peer    tgclient.InputPeer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	clk := clock.NewFake(time.Unix(baseTime, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: userID, FirstName: "Ada", Username: "ada"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	peer, err := network.SeedChannel(channelID, "bucket", "", client)
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return &world{clk: clk, network: network, client: client, peer: peer}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingSleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *recordingSleeper) Sleep(ctx context.Context, wait time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, wait)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *recordingSleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

func patternedBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*31 + i/251)
	}
	return out
}

func newBody(n int) (*bytes.Reader, []byte) {
	data := patternedBytes(n)
	return bytes.NewReader(data), data
}
