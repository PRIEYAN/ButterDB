package phase04

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func floodFault(wait time.Duration) faketg.Fault {
	return faketg.Fault{Kind: faketg.FaultFloodWait, Wait: wait}
}

func dropFault() faketg.Fault {
	return faketg.Fault{Kind: faketg.FaultTransportDrop}
}

func queue(t *testing.T, w *world, faults ...faketg.Fault) {
	t.Helper()
	if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, faults...); err != nil {
		t.Fatalf("queue faults: %v", err)
	}
}

func newControlPolicy(t *testing.T, w *world, sleeper flow.Sleeper) flow.FloodWaitRetryPolicy {
	t.Helper()
	control, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: config.Default().Telegram.SendRate,
		Clock:    w.clk,
		Sleeper:  sleeper,
		Random:   cryptorand.Reader,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewFlowControl: %v", err)
	}
	return control.ControlRetry
}

func sendOnce(w *world, policy flow.FloodWaitRetryPolicy, randomID int64) (int64, error) {
	var msgID int64
	err := policy.Do(context.Background(), func() error {
		var err error
		msgID, err = w.client.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{
			Peer: w.peer, Text: "TDX1|t=put", RandomID: randomID,
		})
		return err
	})
	return msgID, err
}

func TestFloodWaitAndTransportRetryBudgetsAreSeparate(t *testing.T) {
	flood := floodFault(10 * time.Second)
	cases := []struct {
		name        string
		faults      []faketg.Fault
		wantErr     func(error) bool
		wantCalls   int
		wantFlood   int
		wantSucceed bool
	}{
		{
			name:        "both budgets are spent in full and the send still lands",
			faults:      []faketg.Fault{flood, dropFault(), flood, dropFault(), dropFault(), dropFault()},
			wantCalls:   7,
			wantFlood:   2,
			wantSucceed: true,
		},
		{
			name:      "one flood wait too many is returned as a flood wait",
			faults:    []faketg.Fault{flood, flood, flood},
			wantErr:   func(err error) bool { return errors.Is(err, tgclient.ErrFloodWait) },
			wantCalls: 3,
			wantFlood: 2,
		},
		{
			name:      "one transport drop too many is returned as a transport failure",
			faults:    []faketg.Fault{dropFault(), dropFault(), dropFault(), dropFault(), dropFault()},
			wantErr:   func(err error) bool { return errors.Is(err, faketg.ErrInjectedTransport) },
			wantCalls: 5,
		},
		{
			name:      "a flood wait above the per-wait ceiling is not slept at all",
			faults:    []faketg.Fault{floodFault(31 * time.Second)},
			wantErr:   func(err error) bool { return errors.Is(err, tgclient.ErrFloodWait) },
			wantCalls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			sleeper := &recordingSleeper{}
			policy := newControlPolicy(t, w, sleeper)
			queue(t, w, tc.faults...)

			msgID, err := sendOnce(w, policy, mustRandomID(t, "job-1", "put"))
			switch {
			case tc.wantSucceed && (err != nil || msgID == 0):
				t.Fatalf("send = %d, %v; want success", msgID, err)
			case !tc.wantSucceed && !tc.wantErr(err):
				t.Fatalf("send error = %v", err)
			}
			if got := w.client.CallCount(faketg.MethodSendControlIdempotent); got != tc.wantCalls {
				t.Fatalf("telegram calls = %d, want %d", got, tc.wantCalls)
			}
			floodSleeps := 0
			for _, wait := range sleeper.recorded() {
				if wait == 10*time.Second {
					floodSleeps++
				}
			}
			if floodSleeps != tc.wantFlood {
				t.Fatalf("flood wait sleeps = %d (waits %v), want %d", floodSleeps, sleeper.recorded(), tc.wantFlood)
			}
		})
	}

	t.Run("flood waits are slept in full, never shortened", func(t *testing.T) {
		w := newWorld(t)
		sleeper := &recordingSleeper{}
		policy := newControlPolicy(t, w, sleeper)
		queue(t, w, floodFault(7*time.Second))
		if _, err := sendOnce(w, policy, 4001); err != nil {
			t.Fatalf("send: %v", err)
		}
		if waits := sleeper.recorded(); len(waits) != 1 || waits[0] != 7*time.Second {
			t.Fatalf("sleeps = %v, want exactly one 7s flood wait", waits)
		}
	})
}
