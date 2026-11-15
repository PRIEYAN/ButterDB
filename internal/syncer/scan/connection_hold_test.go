package scan

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const probeTimeout = time.Second

type probingHistory struct {
	HistoryReader
	env             *env
	probes          int
	stalls          int
	failWhenSpooled bool
	spoolSeen       bool
	maxSpool        int
}

func (p *probingHistory) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	p.probe()
	p.watchSpool()
	if p.failWhenSpooled && p.spoolSeen {
		return nil, errors.New("injected mid-scan history failure")
	}
	return p.HistoryReader.GetHistory(ctx, peer, offsetID, limit)
}

// A write that cannot start within the timeout means the scan holds the single writer across this network call.
func (p *probingHistory) probe() {
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	p.probes++
	err := p.env.db.Write(probeCtx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(probeCtx, `UPDATE channels SET title = title WHERE channel_id = ?`, registryID)
		return err
	})
	if err != nil {
		p.stalls++
	}
}

func (p *probingHistory) watchSpool() {
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var n int
	err := p.env.db.Read(probeCtx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(probeCtx, `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = ?`, registryID).Scan(&n)
	})
	if err != nil || n == 0 {
		return
	}
	p.spoolSeen = true
	p.maxSpool = max(p.maxSpool, n)
}

func probingEnv(t *testing.T) (*env, *probingHistory) {
	t.Helper()
	var probing *probingHistory
	var e *env
	e = newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		probing = &probingHistory{HistoryReader: base}
		return probing
	}))
	probing.env = e
	return e, probing
}

func seedBuckets(e *env, count int) {
	for n := 1; n <= count; n++ {
		e.seed(registryID, bucketCreateText(e.t, n))
	}
}

func requireAllBuckets(t *testing.T, e *env, count int) {
	t.Helper()
	for n := 1; n <= count; n++ {
		if _, ok := e.bucketPolicy(int64(5000 + n)); !ok {
			t.Fatalf("bucket %d missing after the scan", n)
		}
	}
}

func TestFullHistoryScanLeavesWritersUnblockedDuringFetches(t *testing.T) {
	e, probing := probingEnv(t)
	seedBuckets(e, defaultPageSize+20)

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	if probing.probes < 3 {
		t.Fatalf("probes = %d, want the scan to fetch at least three pages", probing.probes)
	}
	if probing.stalls != 0 {
		t.Fatalf("%d of %d unrelated writes were starved while the scan fetched history", probing.stalls, probing.probes)
	}
	if !e.state(registryID).InitialSyncDone {
		t.Fatal("scan completed without marking the channel authoritative")
	}
	requireAllBuckets(t, e, defaultPageSize+20)
}

func TestIncrementalScanLeavesWritersUnblockedDuringFetches(t *testing.T) {
	e, probing := probingEnv(t)
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatal(err)
	}
	seedBuckets(e, defaultPageSize+20)
	probing.probes, probing.stalls = 0, 0

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("incremental refresh: %v", err)
	}
	if probing.probes < 3 || probing.stalls != 0 {
		t.Fatalf("probes=%d stalls=%d, want at least three fetches and no starved write", probing.probes, probing.stalls)
	}
	requireAllBuckets(t, e, defaultPageSize+20)
}

func TestInitialSyncOfEmptyChannelLeavesWritersUnblockedDuringReads(t *testing.T) {
	e, probing := probingEnv(t)
	for i := 0; i < defaultPageSize+20; i++ {
		e.seed(registryID, "chat noise")
	}
	if err := e.scanner.InitialSyncEmptyChannel(context.Background(), registryID); err != nil {
		t.Fatalf("InitialSyncEmptyChannel() error = %v", err)
	}
	if probing.stalls != 0 {
		t.Fatalf("%d of %d unrelated writes were starved", probing.stalls, probing.probes)
	}
}

func TestFullHistoryScanKeepsFetchedHistoryOutOfMemory(t *testing.T) {
	e, probing := probingEnv(t)
	seedBuckets(e, 2*defaultPageSize+20)

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	if !probing.spoolSeen {
		t.Fatal("no page was spooled before the scan finished fetching; history is being buffered in memory")
	}
	if probing.maxSpool < defaultPageSize {
		t.Fatalf("peak spooled rows = %d, want a full page parked on disk", probing.maxSpool)
	}
	if rows := e.count(`SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = ?`, registryID); rows != 0 {
		t.Fatalf("spool left %d rows behind after a successful scan", rows)
	}
}

func TestInterruptedFullHistoryScanLeavesNoProjectionState(t *testing.T) {
	e, probing := probingEnv(t)
	seedBuckets(e, defaultPageSize+20)
	probing.failWhenSpooled = true

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err == nil {
		t.Fatal("EnsureAuthoritative() error = nil, want the injected failure")
	}
	if !probing.spoolSeen {
		t.Fatal("test never reached a fetch with history already spooled")
	}
	if !e.channelEmpty() {
		t.Fatal("an interrupted scan left projection state behind")
	}
	if e.state(registryID).InitialSyncDone {
		t.Fatal("an interrupted scan marked the channel authoritative")
	}

	probing.failWhenSpooled = false
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("retry after interruption: %v", err)
	}
	requireAllBuckets(t, e, defaultPageSize+20)
}
