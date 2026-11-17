package rpc

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func TestListOwnedChannelsWaitsOutFloodWait(t *testing.T) {
	calls := 0
	raw := handlerInvoker(func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		calls++
		if calls == 1 {
			return &tgclient.FloodWaitError{Duration: 21 * time.Second}
		}
		return reply(output, &tg.MessagesDialogs{})
	})
	var slept []time.Duration
	client, err := New(Dependencies{
		Invoker: fakeScope{raw: raw}, Gate: &recordingGate{}, Random: bytes.NewReader(make([]byte, 64)),
		Sleeper: flow.SleeperFunc(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListOwnedChannels(context.Background()); err != nil {
		t.Fatalf("ListOwnedChannels: %v", err)
	}
	if len(slept) != 1 || slept[0] != 21*time.Second {
		t.Fatalf("slept %v, want one 21s wait", slept)
	}
}

func TestListOwnedChannelsGivesUpOnExcessiveFloodWait(t *testing.T) {
	raw := handlerInvoker(func(context.Context, bin.Encoder, bin.Decoder) error {
		return &tgclient.FloodWaitError{Duration: time.Hour}
	})
	client, err := New(Dependencies{
		Invoker: fakeScope{raw: raw}, Gate: &recordingGate{}, Random: bytes.NewReader(make([]byte, 64)),
		Sleeper: flow.SleeperFunc(func(context.Context, time.Duration) error { t.Fatal("should not sleep an hour"); return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListOwnedChannels(context.Background()); !errors.Is(err, tgclient.ErrFloodWait) {
		var fw *tgclient.FloodWaitError
		if !errors.As(err, &fw) {
			t.Fatalf("err = %v, want flood wait", err)
		}
	}
}
