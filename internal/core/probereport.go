package core

import (
	"sort"
	"time"
)

const (
	probeReportVersion = 1
	probeChannelTitle  = "butterDB probe"
	probeChannelAbout  = "butterDB tgprobe scratch channel"
	probeStepControl   = "control"
	probeStepUpload    = "upload"
	probeStepDownload  = "download"
	probeStepHistory   = "history"
	probeStepDelete    = "delete"
	bytesPerMegabyte   = 1_000_000

	// D4 sizing: about 1 TB over 500,000 objects in the first year.
	probeAverageObjectBytes = 2 << 20
)

type ProbeReport struct {
	ProbeVersion  int              `json:"probe_version"`
	BinaryVersion string           `json:"binary_version"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at"`
	AccountUserID int64            `json:"account_user_id"`
	HomeDC        int              `json:"home_dc"`
	Steps         ProbeStepReports `json:"steps"`
	Recommended   ProbeRecommended `json:"recommended"`
}

type ProbeStepReports struct {
	Control  *ProbeControlReport  `json:"control,omitempty"`
	Upload   *ProbeUploadReport   `json:"upload,omitempty"`
	Download *ProbeDownloadReport `json:"download,omitempty"`
	History  *ProbeHistoryReport  `json:"history,omitempty"`
	Delete   *ProbeDeleteReport   `json:"delete,omitempty"`
}

type ProbeControlReport struct {
	Sent                   int  `json:"sent"`
	FirstFloodWaitSeconds  *int `json:"first_flood_wait_seconds"`
	RateAtFloodPerMinute   *int `json:"rate_at_flood_per_minute"`
	SustainedRatePerMinute int  `json:"sustained_rate_per_minute"`
}

type ProbeUploadRun struct {
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	MBPerS  float64 `json:"mb_per_s"`
}

type ProbeUploadReport struct {
	Runs       []ProbeUploadRun `json:"runs"`
	FloodWaits []int            `json:"flood_waits"`
}

type ProbeDownloadRun struct {
	Concurrency int     `json:"concurrency"`
	MBPerS      float64 `json:"mb_per_s"`
}

type ProbeDownloadReport struct {
	Runs []ProbeDownloadRun `json:"runs"`
}

type ProbeHistoryReport struct {
	Calls                 int     `json:"calls"`
	Seconds               float64 `json:"seconds"`
	CallsPerS             float64 `json:"calls_per_s"`
	FirstFloodWaitSeconds *int    `json:"first_flood_wait_seconds"`
}

type ProbeDeleteReport struct {
	Batches int   `json:"batches"`
	P50Ms   int64 `json:"p50_ms"`
	P95Ms   int64 `json:"p95_ms"`
}

type ProbeRecommended struct {
	ControlPerMinute   int `json:"control_per_minute"`
	DocumentsPerMinute int `json:"documents_per_minute"`
}

func percentileMillis(samples []time.Duration, percentile int) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted)*percentile + 99) / 100
	index = min(max(index, 1), len(sorted)) - 1
	return sorted[index].Milliseconds()
}

func megabytesPerSecond(bytes int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) / bytesPerMegabyte / elapsed.Seconds()
}

func recommendControlRate(floodRate *int, sustained, current int) int {
	if floodRate == nil {
		return current
	}
	return min(max(*floodRate/2, 1), max(sustained, 1))
}

func recommendDocumentRate(current int, runs []ProbeUploadRun, averageObjectBytes int64, floodRate *int) int {
	rate := current
	if best := bestUploadRate(runs); best > 0 && averageObjectBytes > 0 {
		perMinute := int(best * 60 * bytesPerMegabyte / float64(averageObjectBytes))
		rate = min(rate, max(perMinute, 1))
	}
	if floodRate != nil {
		rate = min(rate, max(*floodRate/2, 1))
	}
	return rate
}

func bestUploadRate(runs []ProbeUploadRun) float64 {
	best := 0.0
	for _, run := range runs {
		best = max(best, run.MBPerS)
	}
	return best
}
