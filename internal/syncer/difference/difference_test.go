package difference

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestIncrementalBootstrapsPtsThenUsesDifference(t *testing.T) {
	e := newEnv(t)
	e.seedOp(10, putOp("a.png", 9, 10))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	ensure, full := e.scanner.counts()
	if ensure != 1 || full != 0 || e.tg.CallCount(faketg.MethodGetChannelDifference) != 0 {
		t.Fatalf("scan pass: ensure=%d full=%d diff=%d, want 1/0/0", ensure, full, e.tg.CallCount(faketg.MethodGetChannelDifference))
	}
	if state := e.state(); !state.InitialSyncDone || state.Pts <= 0 {
		t.Fatalf("pts not bootstrapped: %+v", state)
	}

	e.seedOp(20, putOp("b.png", 19, 20))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("difference sync: %v", err)
	}
	ensure, _ = e.scanner.counts()
	if ensure != 1 || e.tg.CallCount(faketg.MethodGetChannelDifference) == 0 {
		t.Fatalf("difference pass: ensure=%d diff=%d, want 1/>0", ensure, e.tg.CallCount(faketg.MethodGetChannelDifference))
	}
	for key, want := range map[string]int64{"a.png": 10, "b.png": 20} {
		if got := e.livePut(key); got != want {
			t.Fatalf("live put of %s = %d, want %d", key, got, want)
		}
	}
	state := e.state()
	if state.Pts != e.networkPts() {
		t.Fatalf("pts = %d, want %d", state.Pts, e.networkPts())
	}
	if state.LastSyncedMsg != 20 {
		t.Fatalf("watermark = %d, want 20", state.LastSyncedMsg)
	}
}

func TestDifferenceAppliesDeletionsAndSkipsExistenceCheck(t *testing.T) {
	e := newEnv(t)
	e.seedOp(10, putOp("x.png", 9, 10))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if _, err := e.net.DeleteOutOfBand(bucketChannel, 10); err != nil {
		t.Fatalf("simulate external delete: %v", err)
	}

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("difference sync: %v", err)
	}
	if got := e.count(`SELECT COUNT(*) FROM deleted_messages WHERE channel_id = ? AND msg_id = 10 AND source = 'external' AND recorded_at = 1700000500000`, bucketChannel); got != 1 {
		t.Fatalf("external deletion rows = %d, want 1", got)
	}
	if got := e.livePut("x.png"); got != 0 {
		t.Fatalf("object still live after difference reported its deletion: put %d", got)
	}
	if calls := e.tg.CallCount(faketg.MethodGetMessages); calls != 0 {
		t.Fatalf("existence check ran %d times, want 0", calls)
	}
	if state := e.state(); state.Pts != e.networkPts() {
		t.Fatalf("pts = %d, want %d", state.Pts, e.networkPts())
	}
}

func TestDifferenceTooLongFallsBackToScan(t *testing.T) {
	e := newEnv(t)
	e.seedOp(10, putOp("a.png", 9, 10))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	if err := e.tg.QueueFaults(faketg.MethodGetChannelDifference, faketg.Fault{Kind: faketg.FaultDifferenceTooLong}); err != nil {
		t.Fatalf("queue fault: %v", err)
	}
	e.seedOp(20, putOp("b.png", 19, 20))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("fallback sync: %v", err)
	}
	if got := e.livePut("b.png"); got != 20 {
		t.Fatalf("object missing after scan fallback: put %d", got)
	}
	if _, full := e.scanner.counts(); full != 1 {
		t.Fatalf("full scans = %d, want 1", full)
	}
	if state := e.state(); state.Pts != e.networkPts() {
		t.Fatalf("pts not re-bootstrapped after fallback: %d, want %d", state.Pts, e.networkPts())
	}
	if calls := e.tg.CallCount(faketg.MethodGetMessages); calls != 0 {
		t.Fatalf("difference path ran an existence check %d times", calls)
	}
}

func TestDifferenceIgnoresMessagesAlreadyScanned(t *testing.T) {
	e := newEnv(t)
	e.seedOp(10, putOp("a.png", 9, 10))
	e.seedOp(20, putOp("b.png", 19, 20))
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	e.setCursor(20, 1, true)
	checksBefore, _ := e.repairs.counts()

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("difference sync: %v", err)
	}
	state := e.state()
	if state.NeedsProjectionRebuild {
		t.Fatal("already-scanned messages flagged a rebuild")
	}
	if state.Pts != e.networkPts() || state.Pts <= 1 {
		t.Fatalf("pts = %d, want %d", state.Pts, e.networkPts())
	}
	if checks, repaired := e.repairs.counts(); checks != checksBefore || len(repaired) != 0 {
		t.Fatalf("replayed messages triggered repair: checks=%d (was %d) repaired=%v", checks, checksBefore, repaired)
	}
	if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?`, bucketChannel); got != 2 {
		t.Fatalf("replay_log rows = %d, want 2", got)
	}
}
