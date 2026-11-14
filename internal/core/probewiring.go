package core

import (
	"context"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

const probeUploadFloodCeiling = 60 * time.Second

var _ tgclient.WriteGate = directGate{}

type directGate struct{}

// The tgprobe control step deliberately bypasses the send limiter to find where Telegram's own flood wait starts.
func (directGate) Do(ctx context.Context, _ tgclient.WriteClass, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return action()
}

func (t *Transport) probeSenders(deps FlowDependencies) (controls, documents *send.Sender, err error) {
	noRetry := withInjections(flow.FloodWaitRetryPolicy{}, deps)
	controls, err = send.New(send.Dependencies{
		Invoker:      t.Connection,
		Writes:       directGate{},
		Slots:        t.Flow.UploadSlots,
		UploadRetry:  noRetry,
		ControlRetry: noRetry,
		Logger:       deps.Logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("core: probe: control sender: %w", err)
	}
	upload := withInjections(flow.DefaultWriteFloodWaitRetryPolicy(), deps)
	upload.MaxWait = probeUploadFloodCeiling
	documents, err = send.New(send.Dependencies{
		Invoker:      t.Connection,
		Writes:       t.Flow.Writes,
		Slots:        t.Flow.UploadSlots,
		UploadRetry:  upload,
		ControlRetry: t.Flow.ControlRetry,
		Logger:       deps.Logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("core: probe: document sender: %w", err)
	}
	return controls, documents, nil
}

func (s *TelegramSession) NewProbe(ctx context.Context, jobID string) (*Probe, error) {
	transport, err := s.Transport(ctx)
	if err != nil {
		return nil, err
	}
	controls, documents, err := transport.probeSenders(FlowDependencies{
		Clock:   s.deps.Clock,
		Sleeper: s.deps.Sleeper,
		Random:  s.deps.Random,
		Logger:  s.deps.Logger,
	})
	if err != nil {
		return nil, err
	}
	return NewProbe(ProbeDependencies{
		Controls:  controls,
		Documents: documents,
		Reader:    transport.Reader,
		Channels:  transport.Channels,
		Clock:     s.deps.Clock,
		Sleeper:   s.deps.Sleeper,
		Random:    s.deps.Random,
		Logger:    s.deps.Logger,
		JobID:     jobID,
	})
}

func (s *TelegramSession) ProbeOptions(binaryVersion string) ProbeOptions {
	return DefaultProbeOptions(s.deps.Config.Telegram.SendRate, binaryVersion)
}
