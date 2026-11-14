package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

const (
	probeControlTextLength = 200
	probeDeleteBatch       = 100
	probeHistoryPage       = 100
	probeDownloadBlock     = 1 << 20
)

var (
	ErrProbeUnknownStep   = errors.New("core: unknown probe step")
	ErrProbeNeedsUpload   = errors.New("core: probe download step needs the upload step")
	ErrProbeNothingToRead = errors.New("core: probe has no uploaded document to read")
)

var probeStepOrder = []string{probeStepControl, probeStepUpload, probeStepDownload, probeStepHistory, probeStepDelete}

type ProbeControlSender interface {
	SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error)
}

type ProbeDocumentSender interface {
	SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error)
}

type ProbeReader interface {
	ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error)
	ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error)
}

type ProbeChannels interface {
	ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error)
	CreateChannel(ctx context.Context, title, about string) (tgclient.InputPeer, error)
	GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error)
	DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error
}

type ProbeDependencies struct {
	Controls  ProbeControlSender
	Documents ProbeDocumentSender
	Reader    ProbeReader
	Channels  ProbeChannels
	Clock     clock.Clock
	Sleeper   flow.Sleeper
	Random    io.Reader
	Logger    *slog.Logger
	JobID     string
}

type ProbePhase struct {
	Interval time.Duration
	Sends    int
}

type ProbeOptions struct {
	Steps                     []string
	ControlPhases             []ProbePhase
	ControlMaxMessages        int
	SmallUploads              int
	SmallUploadBytes          int64
	LargeUploadBytes          int64
	UploadFloodAbort          time.Duration
	DownloadBytes             int64
	DownloadConcurrency       []int
	HistoryMaxCalls           int
	AverageObjectBytes        int64
	CurrentControlPerMinute   int
	CurrentDocumentsPerMinute int
	BinaryVersion             string
}

func DefaultProbeOptions(rate config.SendRateConfig, binaryVersion string) ProbeOptions {
	return ProbeOptions{
		Steps: probeStepOrder,
		ControlPhases: []ProbePhase{
			{Interval: 6 * time.Second, Sends: 20},
			{Interval: 3 * time.Second, Sends: 40},
			{Interval: 2 * time.Second, Sends: 60},
			{Interval: time.Second, Sends: 120},
			{Interval: 500 * time.Millisecond, Sends: 240},
		},
		ControlMaxMessages:        300,
		SmallUploads:              3,
		SmallUploadBytes:          64 << 20,
		LargeUploadBytes:          1 << 30,
		UploadFloodAbort:          60 * time.Second,
		DownloadBytes:             64 << 20,
		DownloadConcurrency:       []int{1, 4, 8, 12},
		HistoryMaxCalls:           200,
		AverageObjectBytes:        probeAverageObjectBytes,
		CurrentControlPerMinute:   rate.ControlPerMinute,
		CurrentDocumentsPerMinute: rate.DocumentsPerMinute,
		BinaryVersion:             binaryVersion,
	}
}

type Probe struct {
	deps ProbeDependencies
}

func NewProbe(deps ProbeDependencies) (*Probe, error) {
	switch {
	case deps.Controls == nil:
		return nil, fmt.Errorf("core: probe: %w: controls", errs.ErrMissingDependency)
	case deps.Documents == nil:
		return nil, fmt.Errorf("core: probe: %w: documents", errs.ErrMissingDependency)
	case deps.Reader == nil:
		return nil, fmt.Errorf("core: probe: %w: reader", errs.ErrMissingDependency)
	case deps.Channels == nil:
		return nil, fmt.Errorf("core: probe: %w: channels", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("core: probe: %w: clock", errs.ErrMissingDependency)
	case deps.Sleeper == nil:
		return nil, fmt.Errorf("core: probe: %w: sleeper", errs.ErrMissingDependency)
	case deps.Random == nil:
		return nil, fmt.Errorf("core: probe: %w: random", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("core: probe: %w: logger", errs.ErrMissingDependency)
	case deps.JobID == "":
		return nil, fmt.Errorf("core: probe: %w: job id", errs.ErrMissingDependency)
	}
	return &Probe{deps: deps}, nil
}

func ValidateProbeSteps(steps []string) error {
	for _, step := range steps {
		if !slices.Contains(probeStepOrder, step) {
			return fmt.Errorf("%w: %q", ErrProbeUnknownStep, step)
		}
	}
	if slices.Contains(steps, probeStepDownload) && !slices.Contains(steps, probeStepUpload) {
		return ErrProbeNeedsUpload
	}
	return nil
}

func ParseProbeSteps(list string) []string {
	if strings.TrimSpace(list) == "" {
		return probeStepOrder
	}
	var steps []string
	for _, part := range strings.Split(list, ",") {
		steps = append(steps, strings.TrimSpace(part))
	}
	return steps
}

type probeRun struct {
	probe   *Probe
	opts    ProbeOptions
	peer    tgclient.InputPeer
	tracked []int64
	large   int64
	report  *ProbeReport
}

func (p *Probe) Run(ctx context.Context, account tgclient.Account, opts ProbeOptions) (ProbeReport, error) {
	if err := ValidateProbeSteps(opts.Steps); err != nil {
		return ProbeReport{}, err
	}
	report := ProbeReport{
		ProbeVersion:  probeReportVersion,
		BinaryVersion: opts.BinaryVersion,
		StartedAt:     p.deps.Clock.Now().UTC(),
		AccountUserID: account.UserID,
	}
	run := &probeRun{probe: p, opts: opts, report: &report}
	err := run.execute(ctx)
	cleanupErr := run.cleanup(ctx)
	report.FinishedAt = p.deps.Clock.Now().UTC()
	report.Recommended = run.recommend()
	return report, errors.Join(err, cleanupErr)
}

func (r *probeRun) execute(ctx context.Context) error {
	peer, err := r.ensureChannel(ctx)
	if err != nil {
		return err
	}
	r.peer = peer
	runners := map[string]func(context.Context) error{
		probeStepControl:  r.runControl,
		probeStepUpload:   r.runUpload,
		probeStepDownload: r.runDownload,
		probeStepHistory:  r.runHistory,
		probeStepDelete:   r.runDelete,
	}
	for _, step := range probeStepOrder {
		if !slices.Contains(r.opts.Steps, step) {
			continue
		}
		if err := runners[step](ctx); err != nil {
			return fmt.Errorf("core: probe %s: %w", step, err)
		}
	}
	return nil
}

func (r *probeRun) ensureChannel(ctx context.Context) (tgclient.InputPeer, error) {
	channels, err := r.probe.deps.Channels.ListOwnedChannels(ctx)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("core: probe: list channels: %w", err)
	}
	for _, channel := range channels {
		if channel.Title == probeChannelTitle {
			return channel.Peer, nil
		}
	}
	peer, err := r.probe.deps.Channels.CreateChannel(ctx, probeChannelTitle, probeChannelAbout)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("core: probe: create channel: %w", err)
	}
	return peer, nil
}

func (r *probeRun) randomID(purpose string) (int64, error) {
	id, err := send.StableRandomID(r.probe.deps.JobID, purpose)
	if err != nil {
		return 0, fmt.Errorf("core: probe: random id: %w", err)
	}
	return id, nil
}

func (r *probeRun) track(msgID int64) {
	if msgID > 0 {
		r.tracked = append(r.tracked, msgID)
	}
}

func floodSeconds(err error) (int, bool) {
	var wait *tgclient.FloodWaitError
	if errors.As(err, &wait) {
		return int(wait.Duration / time.Second), true
	}
	return 0, false
}

func (r *probeRun) runControl(ctx context.Context) error {
	report := &ProbeControlReport{}
	r.report.Steps.Control = report
	text := strings.Repeat("x", probeControlTextLength)
	for _, phase := range r.opts.ControlPhases {
		perMinute := int(time.Minute / phase.Interval)
		for i := 0; i < phase.Sends; i++ {
			if report.Sent >= r.opts.ControlMaxMessages {
				report.SustainedRatePerMinute = perMinute
				return nil
			}
			if err := r.sendControl(ctx, report, text); err != nil {
				return r.settleControlFlood(report, perMinute, err)
			}
			if err := r.probe.deps.Sleeper.Sleep(ctx, phase.Interval); err != nil {
				return err
			}
		}
		report.SustainedRatePerMinute = perMinute
	}
	return nil
}

func (r *probeRun) sendControl(ctx context.Context, report *ProbeControlReport, text string) error {
	randomID, err := r.randomID(fmt.Sprintf("control-%d", report.Sent))
	if err != nil {
		return err
	}
	msgID, err := r.probe.deps.Controls.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: r.peer, Text: text, RandomID: randomID})
	if err != nil {
		return err
	}
	r.track(msgID)
	report.Sent++
	return nil
}

func (r *probeRun) settleControlFlood(report *ProbeControlReport, perMinute int, err error) error {
	seconds, flood := floodSeconds(err)
	if !flood {
		return err
	}
	report.FirstFloodWaitSeconds = &seconds
	report.RateAtFloodPerMinute = &perMinute
	r.probe.deps.Logger.Info("tgprobe: control step reached a flood wait", "seconds", seconds, "rate_per_minute", perMinute)
	return nil
}

func (r *probeRun) uploadSizes() []int64 {
	sizes := make([]int64, 0, r.opts.SmallUploads+1)
	for i := 0; i < r.opts.SmallUploads; i++ {
		sizes = append(sizes, r.opts.SmallUploadBytes)
	}
	return append(sizes, r.opts.LargeUploadBytes)
}

func (r *probeRun) runUpload(ctx context.Context) error {
	report := &ProbeUploadReport{FloodWaits: []int{}}
	r.report.Steps.Upload = report
	sizes := r.uploadSizes()
	for i, size := range sizes {
		if stop, err := r.uploadOne(ctx, report, i, size, i == len(sizes)-1); err != nil || stop {
			return err
		}
	}
	return nil
}

func (r *probeRun) uploadOne(ctx context.Context, report *ProbeUploadReport, index int, size int64, last bool) (stop bool, err error) {
	randomID, err := r.randomID(fmt.Sprintf("upload-%d", index))
	if err != nil {
		return true, err
	}
	body, err := newProbeBody(r.probe.deps.Random, size)
	if err != nil {
		return true, err
	}
	started := r.probe.deps.Clock.Now()
	result, err := r.probe.deps.Documents.SendFileIdempotent(ctx, tgclient.SendFileRequest{
		Peer: r.peer, Caption: "butterDB probe", FileName: fmt.Sprintf("probe-%d.bin", index), Size: size, Body: body, RandomID: randomID,
	})
	if err != nil {
		return r.settleUploadFlood(report, err)
	}
	elapsed := r.probe.deps.Clock.Now().Sub(started)
	r.track(result.MsgID)
	report.Runs = append(report.Runs, ProbeUploadRun{Bytes: size, Seconds: elapsed.Seconds(), MBPerS: megabytesPerSecond(size, elapsed)})
	if result.Document.DCID != 0 {
		r.report.HomeDC = result.Document.DCID
	}
	if last {
		r.large = result.MsgID
	}
	return false, nil
}

func (r *probeRun) settleUploadFlood(report *ProbeUploadReport, err error) (bool, error) {
	seconds, flood := floodSeconds(err)
	if !flood {
		return true, err
	}
	report.FloodWaits = append(report.FloodWaits, seconds)
	return true, nil
}

func (r *probeRun) runDownload(ctx context.Context) error {
	report := &ProbeDownloadReport{}
	r.report.Steps.Download = report
	if r.large == 0 {
		return ErrProbeNothingToRead
	}
	ref, err := r.probe.deps.Reader.ResolveDocument(ctx, r.peer, r.large)
	if err != nil {
		return fmt.Errorf("resolve probe document: %w", err)
	}
	total := min(r.opts.DownloadBytes, ref.Size)
	for _, concurrency := range r.opts.DownloadConcurrency {
		started := r.probe.deps.Clock.Now()
		if err := r.readConcurrently(ctx, ref, total, concurrency); err != nil {
			return err
		}
		elapsed := r.probe.deps.Clock.Now().Sub(started)
		report.Runs = append(report.Runs, ProbeDownloadRun{Concurrency: concurrency, MBPerS: megabytesPerSecond(total, elapsed)})
	}
	return nil
}

func (r *probeRun) readConcurrently(ctx context.Context, ref tgclient.DocumentRef, total int64, concurrency int) error {
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, concurrency)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, probeDownloadBlock)
			for readCtx.Err() == nil {
				offset := next.Add(probeDownloadBlock) - probeDownloadBlock
				if offset >= total {
					return
				}
				want := buf[:min(probeDownloadBlock, total-offset)]
				if _, err := r.probe.deps.Reader.ReadDocumentRange(readCtx, ref, offset, want); err != nil {
					errc <- err
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errc)
	if err := <-errc; err != nil {
		return fmt.Errorf("read probe document: %w", err)
	}
	return ctx.Err()
}

func (r *probeRun) runHistory(ctx context.Context) error {
	report := &ProbeHistoryReport{}
	r.report.Steps.History = report
	started := r.probe.deps.Clock.Now()
	var offsetID int64
	for report.Calls < r.opts.HistoryMaxCalls {
		page, err := r.probe.deps.Channels.GetHistory(ctx, r.peer, offsetID, probeHistoryPage)
		report.Calls++
		if err != nil {
			if seconds, flood := floodSeconds(err); flood {
				report.FirstFloodWaitSeconds = &seconds
				break
			}
			return err
		}
		offsetID = nextHistoryOffset(page)
	}
	elapsed := r.probe.deps.Clock.Now().Sub(started)
	report.Seconds = elapsed.Seconds()
	if elapsed > 0 {
		report.CallsPerS = float64(report.Calls) / elapsed.Seconds()
	}
	return nil
}

func nextHistoryOffset(page []tgclient.HistoryMessage) int64 {
	if len(page) < probeHistoryPage {
		return 0
	}
	return page[len(page)-1].MsgID
}

func (r *probeRun) runDelete(ctx context.Context) error {
	report := &ProbeDeleteReport{}
	r.report.Steps.Delete = report
	latencies, err := r.deleteTracked(ctx)
	report.Batches = len(latencies)
	report.P50Ms = percentileMillis(latencies, 50)
	report.P95Ms = percentileMillis(latencies, 95)
	return err
}

func (r *probeRun) deleteTracked(ctx context.Context) ([]time.Duration, error) {
	var latencies []time.Duration
	for len(r.tracked) > 0 {
		batch := r.tracked[:min(probeDeleteBatch, len(r.tracked))]
		started := r.probe.deps.Clock.Now()
		if err := r.probe.deps.Channels.DeleteMessages(ctx, r.peer, batch); err != nil {
			return latencies, fmt.Errorf("delete probe messages: %w", err)
		}
		latencies = append(latencies, r.probe.deps.Clock.Now().Sub(started))
		r.tracked = r.tracked[len(batch):]
	}
	return latencies, nil
}

func (r *probeRun) cleanup(ctx context.Context) error {
	if len(r.tracked) == 0 || r.peer.ChannelID == 0 {
		return nil
	}
	if _, err := r.deleteTracked(ctx); err != nil {
		return fmt.Errorf("core: probe cleanup: %w", err)
	}
	return nil
}

func (r *probeRun) recommend() ProbeRecommended {
	recommended := ProbeRecommended{
		ControlPerMinute:   r.opts.CurrentControlPerMinute,
		DocumentsPerMinute: r.opts.CurrentDocumentsPerMinute,
	}
	if control := r.report.Steps.Control; control != nil {
		recommended.ControlPerMinute = recommendControlRate(control.RateAtFloodPerMinute, control.SustainedRatePerMinute, r.opts.CurrentControlPerMinute)
	}
	if upload := r.report.Steps.Upload; upload != nil {
		recommended.DocumentsPerMinute = recommendDocumentRate(r.opts.CurrentDocumentsPerMinute, upload.Runs, r.opts.AverageObjectBytes, r.uploadFloodRate(upload))
	}
	return recommended
}

func (r *probeRun) uploadFloodRate(upload *ProbeUploadReport) *int {
	if len(upload.FloodWaits) == 0 || len(upload.Runs) == 0 {
		return nil
	}
	var elapsed float64
	for _, run := range upload.Runs {
		elapsed += run.Seconds
	}
	if elapsed <= 0 {
		return nil
	}
	rate := int(float64(len(upload.Runs)) * 60 / elapsed)
	return &rate
}
