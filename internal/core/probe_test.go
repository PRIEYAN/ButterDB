package core

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

type probeHarness struct {
	clk     *clock.Fake
	network *faketg.Network
	client  *faketg.Client
	probe   *Probe
}

func newProbeHarness(t *testing.T) *probeHarness {
	t.Helper()
	clk := clock.NewFake(time.Unix(1763000000, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: fakeUserID, FirstName: "Ada", Username: "ada"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	probe, err := NewProbe(ProbeDependencies{
		Controls:  client,
		Documents: client,
		Reader:    client,
		Channels:  client,
		Clock:     clk,
		Sleeper:   flow.SleeperFunc(func(context.Context, time.Duration) error { return nil }),
		Random:    cryptorand.Reader,
		Logger:    newDiscardLogger(),
		JobID:     "probe-job",
	})
	if err != nil {
		t.Fatalf("NewProbe: %v", err)
	}
	return &probeHarness{clk: clk, network: network, client: client, probe: probe}
}

func smallProbeOptions() ProbeOptions {
	return ProbeOptions{
		Steps:                     probeStepOrder,
		ControlPhases:             []ProbePhase{{Interval: 6 * time.Second, Sends: 3}, {Interval: 3 * time.Second, Sends: 3}},
		ControlMaxMessages:        5,
		SmallUploads:              1,
		SmallUploadBytes:          2 << 20,
		LargeUploadBytes:          3 << 20,
		UploadFloodAbort:          time.Minute,
		DownloadBytes:             2 << 20,
		DownloadConcurrency:       []int{1, 2},
		HistoryMaxCalls:           4,
		AverageObjectBytes:        1 << 20,
		CurrentControlPerMinute:   20,
		CurrentDocumentsPerMinute: 6,
		BinaryVersion:             "test",
	}
}

func (h *probeHarness) run(t *testing.T, opts ProbeOptions) (ProbeReport, error) {
	t.Helper()
	return h.probe.Run(context.Background(), tgclient.Account{UserID: fakeUserID}, opts)
}

func (h *probeHarness) probeChannel(t *testing.T) tgclient.Channel {
	t.Helper()
	channels, err := h.client.ListOwnedChannels(context.Background())
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	var found []tgclient.Channel
	for _, channel := range channels {
		if channel.Title == probeChannelTitle {
			found = append(found, channel)
		}
	}
	if len(found) != 1 {
		t.Fatalf("probe channels = %d, want exactly 1", len(found))
	}
	return found[0]
}

func TestNewProbeRejectsMissingDependencies(t *testing.T) {
	h := newProbeHarness(t)
	complete := h.probe.deps
	cases := []struct {
		name   string
		mutate func(*ProbeDependencies)
	}{
		{"nil controls", func(d *ProbeDependencies) { d.Controls = nil }},
		{"nil documents", func(d *ProbeDependencies) { d.Documents = nil }},
		{"nil reader", func(d *ProbeDependencies) { d.Reader = nil }},
		{"nil channels", func(d *ProbeDependencies) { d.Channels = nil }},
		{"nil clock", func(d *ProbeDependencies) { d.Clock = nil }},
		{"nil sleeper", func(d *ProbeDependencies) { d.Sleeper = nil }},
		{"nil random", func(d *ProbeDependencies) { d.Random = nil }},
		{"nil logger", func(d *ProbeDependencies) { d.Logger = nil }},
		{"empty job id", func(d *ProbeDependencies) { d.JobID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := complete
			tc.mutate(&deps)
			if got, err := NewProbe(deps); got != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewProbe = %v, %v; want ErrMissingDependency", got, err)
			}
		})
	}
}

func TestValidateProbeSteps(t *testing.T) {
	cases := []struct {
		name  string
		steps []string
		want  error
	}{
		{"all steps", probeStepOrder, nil},
		{"upload alone", []string{"upload"}, nil},
		{"unknown step", []string{"control", "ping"}, ErrProbeUnknownStep},
		{"download without upload", []string{"download"}, ErrProbeNeedsUpload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateProbeSteps(tc.steps); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateProbeSteps(%v) = %v, want %v", tc.steps, err, tc.want)
			}
		})
	}
	if got := ParseProbeSteps(" control , upload "); len(got) != 2 || got[0] != "control" || got[1] != "upload" {
		t.Fatalf("ParseProbeSteps = %v", got)
	}
	if got := ParseProbeSteps(""); len(got) != len(probeStepOrder) {
		t.Fatalf("empty step list = %v, want every step", got)
	}
}

func TestProbeRunsEveryStepAndLeavesTheChannelEmpty(t *testing.T) {
	h := newProbeHarness(t)
	if err := h.client.SetLatency(faketg.MethodSendFileIdempotent, 2*time.Second); err != nil {
		t.Fatalf("latency: %v", err)
	}
	report, err := h.run(t, smallProbeOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	steps := report.Steps
	if steps.Control == nil || steps.Control.Sent != 5 || steps.Control.FirstFloodWaitSeconds != nil || steps.Control.SustainedRatePerMinute != 20 {
		t.Fatalf("control report = %+v", steps.Control)
	}
	if steps.Upload == nil || len(steps.Upload.Runs) != 2 || steps.Upload.Runs[0].MBPerS <= 0 || len(steps.Upload.FloodWaits) != 0 {
		t.Fatalf("upload report = %+v", steps.Upload)
	}
	if steps.Download == nil || len(steps.Download.Runs) != 2 || steps.Download.Runs[1].Concurrency != 2 {
		t.Fatalf("download report = %+v", steps.Download)
	}
	if steps.History == nil || steps.History.Calls != 4 {
		t.Fatalf("history report = %+v", steps.History)
	}
	if steps.Delete == nil || steps.Delete.Batches != 1 {
		t.Fatalf("delete report = %+v", steps.Delete)
	}
	if report.HomeDC == 0 || report.AccountUserID != fakeUserID || report.ProbeVersion != 1 || report.BinaryVersion != "test" {
		t.Fatalf("report header = %+v", report)
	}
	remaining, err := h.network.Messages(h.probeChannel(t).Peer.ChannelID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("messages left after cleanup = %d, %v", len(remaining), err)
	}
}

func TestProbeReusesItsChannelAcrossRuns(t *testing.T) {
	h := newProbeHarness(t)
	opts := smallProbeOptions()
	opts.Steps = []string{"control"}
	for i := 0; i < 2; i++ {
		if _, err := h.run(t, opts); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	h.probeChannel(t)
}

func TestProbeControlStepStopsAtFirstFloodWait(t *testing.T) {
	h := newProbeHarness(t)
	fault := faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 27 * time.Second}
	if err := h.client.FaultOnCall(faketg.MethodSendControlIdempotent, 4, fault); err != nil {
		t.Fatalf("fault: %v", err)
	}
	opts := smallProbeOptions()
	opts.Steps = []string{"control", "delete"}
	report, err := h.run(t, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	control := report.Steps.Control
	if control.Sent != 3 || control.FirstFloodWaitSeconds == nil || *control.FirstFloodWaitSeconds != 27 {
		t.Fatalf("control report = %+v", control)
	}
	if control.RateAtFloodPerMinute == nil || *control.RateAtFloodPerMinute != 20 || control.SustainedRatePerMinute != 10 {
		t.Fatalf("rates = %+v, want flood at 20/min, sustained 10/min", control)
	}
	if report.Recommended.ControlPerMinute != 10 {
		t.Fatalf("recommended control rate = %d, want 10", report.Recommended.ControlPerMinute)
	}
	if got := h.client.CallCount(faketg.MethodSendControlIdempotent); got != 4 {
		t.Fatalf("control sends = %d, want 4 (no retry after the flood wait)", got)
	}
}

func TestProbeUploadStepStopsAtFloodWait(t *testing.T) {
	h := newProbeHarness(t)
	fault := faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 90 * time.Second}
	if err := h.client.FaultOnCall(faketg.MethodSendFileIdempotent, 2, fault); err != nil {
		t.Fatalf("fault: %v", err)
	}
	opts := smallProbeOptions()
	opts.Steps = []string{"upload"}
	report, err := h.run(t, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	upload := report.Steps.Upload
	if len(upload.Runs) != 1 || len(upload.FloodWaits) != 1 || upload.FloodWaits[0] != 90 {
		t.Fatalf("upload report = %+v", upload)
	}
}

func TestProbeDownloadWithoutUploadedDocumentFails(t *testing.T) {
	h := newProbeHarness(t)
	fault := faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 90 * time.Second}
	if err := h.client.FaultOnCall(faketg.MethodSendFileIdempotent, 1, fault); err != nil {
		t.Fatalf("fault: %v", err)
	}
	opts := smallProbeOptions()
	opts.Steps = []string{"upload", "download"}
	if _, err := h.run(t, opts); !errors.Is(err, ErrProbeNothingToRead) {
		t.Fatalf("Run = %v, want ErrProbeNothingToRead", err)
	}
}

func TestProbeCleansUpWhenAStepFails(t *testing.T) {
	h := newProbeHarness(t)
	boom := errors.New("history unavailable")
	if err := h.client.FaultOnCall(faketg.MethodGetHistory, 1, faketg.Fault{Kind: faketg.FaultError, Err: boom}); err != nil {
		t.Fatalf("fault: %v", err)
	}
	opts := smallProbeOptions()
	opts.Steps = []string{"control", "history"}
	if _, err := h.run(t, opts); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the history error", err)
	}
	remaining, err := h.network.Messages(h.probeChannel(t).Peer.ChannelID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("messages left after failed run = %d, %v", len(remaining), err)
	}
}

func TestProbeRejectsUnknownStepBeforeSending(t *testing.T) {
	h := newProbeHarness(t)
	opts := smallProbeOptions()
	opts.Steps = []string{"ping"}
	if _, err := h.run(t, opts); !errors.Is(err, ErrProbeUnknownStep) {
		t.Fatalf("Run = %v, want ErrProbeUnknownStep", err)
	}
	if got := h.client.CallCount(faketg.MethodListOwnedChannels); got != 0 {
		t.Fatalf("a rejected run made %d telegram calls", got)
	}
}

func TestProbeBodyRewindsToIdenticalBytes(t *testing.T) {
	body, err := newProbeBody(cryptorand.Reader, 100_000)
	if err != nil {
		t.Fatalf("newProbeBody: %v", err)
	}
	first, err := io.ReadAll(body)
	if err != nil || len(first) != 100_000 {
		t.Fatalf("first read = %d bytes, %v", len(first), err)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	second, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("rewound read differs (%v)", err)
	}
	if size, err := body.Seek(0, io.SeekEnd); err != nil || size != 100_000 {
		t.Fatalf("SeekEnd = %d, %v", size, err)
	}
	if _, err := body.Seek(10, io.SeekStart); !errors.Is(err, errProbeBodySeek) {
		t.Fatalf("mid-stream seek = %v, want errProbeBodySeek", err)
	}
}

func TestRecommendations(t *testing.T) {
	flood30 := 30
	flood2 := 2
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"no flood keeps the configured control rate", recommendControlRate(nil, 10, 20), 20},
		{"half the flood rate", recommendControlRate(&flood30, 20, 20), 15},
		{"never above the sustained rate", recommendControlRate(&flood30, 6, 20), 6},
		{"never below one", recommendControlRate(&flood2, 0, 20), 1},
		{"documents capped by throughput", recommendDocumentRate(60, []ProbeUploadRun{{MBPerS: 1}}, 1_000_000, nil), 60},
		{"documents lowered by slow uploads", recommendDocumentRate(60, []ProbeUploadRun{{MBPerS: 0.5}}, 2_000_000, nil), 15},
		{"documents capped at half the flood rate", recommendDocumentRate(60, nil, 1, &flood30), 15},
		{"documents keep config without data", recommendDocumentRate(6, nil, 1, nil), 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %d, want %d", tc.got, tc.want)
			}
		})
	}
}

func TestPercentileMillis(t *testing.T) {
	samples := []time.Duration{400 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}
	cases := []struct {
		name       string
		percentile int
		want       int64
	}{
		{"median", 50, 200},
		{"p95", 95, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentileMillis(samples, tc.percentile); got != tc.want {
				t.Fatalf("p%d = %d, want %d", tc.percentile, got, tc.want)
			}
		})
	}
	if got := percentileMillis(nil, 50); got != 0 {
		t.Fatalf("empty p50 = %d, want 0", got)
	}
}
