package core

import (
	"context"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const writeSendBurst = 5

func newWriteSendFlow(t *testing.T) (*FlowControl, *admission.Controller, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Unix(1763000000, 0))
	sleeper := flow.SleeperFunc(func(ctx context.Context, wait time.Duration) error {
		clk.Advance(wait)
		return ctx.Err()
	})
	flowControl, err := NewFlowControl(FlowDependencies{
		SendRate: config.SendRateConfig{ControlPerMinute: 60, ControlBurst: writeSendBurst, DocumentsPerMinute: 60, DocumentsBurst: writeSendBurst},
		Clock:    clk, Sleeper: sleeper, Random: zeroReader{}, Logger: newDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewFlowControl: %v", err)
	}
	controller, err := admission.New(admission.Dependencies{
		Config:  admission.Config{MaxConcurrentPuts: 2, MaxConcurrentPutsPerKey: 1},
		Pacer:   flowControl.Limiter,
		Clock:   clk,
		Sleeper: sleeper,
	})
	if err != nil {
		t.Fatalf("admission.New: %v", err)
	}
	return flowControl, controller, clk
}

func remainingTokens(limiter *flow.SendLimiter, class flow.SendClass) int {
	remaining := 0
	for {
		delay, cancel := limiter.Reserve(class)
		if delay > 0 {
			cancel()
			return remaining
		}
		remaining++
	}
}

func TestPutSendConsumesExactlyOneLimiterToken(t *testing.T) {
	tests := []struct {
		name  string
		class tgclient.WriteClass
		pace  flow.SendClass
	}{
		{name: "control send", class: tgclient.WriteControl, pace: flow.SendControl},
		{name: "document send", class: tgclient.WriteDocument, pace: flow.SendDocument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flowControl, controller, clk := newWriteSendFlow(t)
			ctx := context.Background()
			budget := admission.Budget{Deadline: clk.Now().Add(time.Hour)}
			if err := controller.WaitSend(ctx, budget, tt.pace); err != nil {
				t.Fatalf("WaitSend: %v", err)
			}
			sent := false
			if err := flowControl.PrepacedWrites.Do(ctx, tt.class, func() error { sent = true; return nil }); err != nil || !sent {
				t.Fatalf("pre-paced send = %v, sent %v", err, sent)
			}
			if got := remainingTokens(flowControl.Limiter, tt.pace); got != writeSendBurst-1 {
				t.Fatalf("tokens left after one put send = %d, want %d", got, writeSendBurst-1)
			}
		})
	}
}

func TestLimitedWritesConsumeOneLimiterTokenEach(t *testing.T) {
	flowControl, _, _ := newWriteSendFlow(t)
	ctx := context.Background()
	if err := flowControl.Writes.Do(ctx, tgclient.WriteDelete, func() error { return nil }); err != nil {
		t.Fatalf("limited write: %v", err)
	}
	if got := remainingTokens(flowControl.Limiter, flow.SendControl); got != writeSendBurst-1 {
		t.Fatalf("tokens left after one limited write = %d, want %d", got, writeSendBurst-1)
	}
}
