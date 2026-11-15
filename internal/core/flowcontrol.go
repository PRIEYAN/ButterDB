package core

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/rangeio"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

var (
	_ tgclient.WriteGate   = (*flow.WriteCoordinator)(nil)
	_ tgclient.GetFileGate = (*flow.GetFileLimiter)(nil)
	_ send.UploadSlots     = (*flow.UploadPartSlots)(nil)
	_ send.RetryPolicy     = flow.FloodWaitRetryPolicy{}
	_ rangeio.BlockRetry   = flow.FloodWaitRetryPolicy{}
)

type FlowDependencies struct {
	SendRate config.SendRateConfig
	Clock    clock.Clock
	Sleeper  flow.Sleeper
	Random   io.Reader
	Logger   *slog.Logger
}

type FlowControl struct {
	Limiter        *flow.SendLimiter
	Writes         *flow.WriteCoordinator
	PrepacedWrites *flow.WriteCoordinator
	GetFile        *flow.GetFileLimiter
	UploadSlots    *flow.UploadPartSlots
	ControlRetry   flow.FloodWaitRetryPolicy
	UploadRetry    flow.FloodWaitRetryPolicy
	BlockRetry     flow.FloodWaitRetryPolicy
}

func NewFlowControl(deps FlowDependencies) (*FlowControl, error) {
	switch {
	case deps.Clock == nil:
		return nil, fmt.Errorf("core: flow control: %w: clock", errs.ErrMissingDependency)
	case deps.Sleeper == nil:
		return nil, fmt.Errorf("core: flow control: %w: sleeper", errs.ErrMissingDependency)
	case deps.Random == nil:
		return nil, fmt.Errorf("core: flow control: %w: random", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("core: flow control: %w: logger", errs.ErrMissingDependency)
	}
	rate := deps.SendRate
	limiter, err := flow.NewSendLimiterWithClock(deps.Clock, deps.Sleeper, rate.ControlPerMinute, rate.ControlBurst, rate.DocumentsPerMinute, rate.DocumentsBurst)
	if err != nil {
		return nil, fmt.Errorf("core: flow control: send limiter: %w", err)
	}
	writes, err := flow.NewWriteCoordinator(flow.WriteCoordinatorDeps{Clock: deps.Clock, Sleeper: deps.Sleeper, Limiter: limiter})
	if err != nil {
		return nil, fmt.Errorf("core: flow control: write coordinator: %w", err)
	}
	control := withInjections(flow.DefaultWriteFloodWaitRetryPolicy(), deps)
	upload := withInjections(flow.DefaultUploadNetworkRetryPolicy(), deps)
	block := withInjections(flow.DefaultWriteFloodWaitRetryPolicy(), deps)
	for name, policy := range map[string]flow.FloodWaitRetryPolicy{"control": control, "upload": upload, "block": block} {
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("core: flow control: %s retry policy: %w", name, err)
		}
	}
	return &FlowControl{
		Limiter:        limiter,
		Writes:         writes,
		PrepacedWrites: writes.Prepaced(),
		GetFile:        flow.NewGetFileLimiter(),
		UploadSlots:    flow.NewUploadPartSlots(),
		ControlRetry:   control,
		UploadRetry:    upload,
		BlockRetry:     block,
	}, nil
}

func withInjections(policy flow.FloodWaitRetryPolicy, deps FlowDependencies) flow.FloodWaitRetryPolicy {
	policy.Sleeper = deps.Sleeper
	policy.Random = deps.Random
	policy.Logger = deps.Logger
	return policy
}
