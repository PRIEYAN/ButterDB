package difference

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func floodFaults(n int, wait time.Duration) []faketg.Fault {
	faults := make([]faketg.Fault, n)
	for i := range faults {
		faults[i] = faketg.Fault{Kind: faketg.FaultFloodWait, Wait: wait}
	}
	return faults
}

func TestIncrementalAppliesOpsAscending(t *testing.T) {
	e := newEnv(t)
	e.reader = reversingReader{inner: e.tg}
	e.build()
	e.setCursor(5, e.networkPts(), true)
	e.seedOp(10, putOp("k1", 9, 10))
	e.seedOp(20, delOp("k1", 20))
	e.seedOp(30, putOp("k2", 29, 30))

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got, want := e.ingest.ingested(), []int64{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ingest order = %v, want %v", got, want)
	}
	if got := e.livePut("k1"); got != 0 {
		t.Fatalf("k1 still live at put %d after its later tombstone", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ? AND object_key = 'k1' AND del_msg_id = 20`, bucketChannel); got != 1 {
		t.Fatalf("k1 tombstone rows = %d, want 1", got)
	}
	if got := e.livePut("k2"); got != 30 {
		t.Fatalf("k2 put = %d, want 30", got)
	}
	if state := e.state(); state.LastSyncedMsg != 30 {
		t.Fatalf("watermark = %d, want 30", state.LastSyncedMsg)
	}
}

func TestIncrementalRetriesReadFloodWait(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	e.seedOp(10, putOp("a", 9, 10))
	if err := e.tg.QueueFaults(faketg.MethodGetChannelDifference, floodFaults(2, 3*time.Second)...); err != nil {
		t.Fatalf("queue faults: %v", err)
	}

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got, want := e.sleeper.recorded(), []time.Duration{3 * time.Second, 3 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("flood waits slept = %v, want %v", got, want)
	}
	if got := e.livePut("a"); got != 10 {
		t.Fatalf("a missing after flood-wait retry: put %d", got)
	}
	if state := e.state(); state.LastSyncedMsg != 10 {
		t.Fatalf("watermark = %d, want 10", state.LastSyncedMsg)
	}
}

func TestIncrementalFailsAfterMaxFloodRetries(t *testing.T) {
	e := newEnv(t)
	pts := e.networkPts()
	e.setCursor(5, pts, true)
	e.seedOp(10, putOp("a", 9, 10))
	if err := e.tg.QueueFaults(faketg.MethodGetChannelDifference, floodFaults(maxFloodWaitRetries+2, time.Second)...); err != nil {
		t.Fatalf("queue faults: %v", err)
	}

	err := e.service.Sync(e.ctx, bucketChannel)
	if !errors.Is(err, tgclient.ErrFloodWait) {
		t.Fatalf("sync error = %v, want flood wait", err)
	}
	if got := len(e.sleeper.recorded()); got != maxFloodWaitRetries {
		t.Fatalf("slept %d times, want %d", got, maxFloodWaitRetries)
	}
	if state := e.state(); state.Pts != pts || state.LastSyncedMsg != 5 {
		t.Fatalf("cursor moved after a failed read: %+v", state)
	}
}

func TestFloodWaitSleepIsCapped(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	if err := e.tg.QueueFaults(faketg.MethodGetChannelDifference, floodFaults(1, 90*time.Second)...); err != nil {
		t.Fatalf("queue faults: %v", err)
	}
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got, want := e.sleeper.recorded(), []time.Duration{maxFloodWaitSleep}; !reflect.DeepEqual(got, want) {
		t.Fatalf("flood waits slept = %v, want %v", got, want)
	}
}

func TestIncrementalIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	e.seedOp(10, putOp("a", 9, 10))

	for pass := 1; pass <= 2; pass++ {
		if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?`, bucketChannel); got != 1 {
		t.Fatalf("replay_log rows = %d, want 1", got)
	}
	if state := e.state(); state.Pts != e.networkPts() || state.LastSyncedMsg != 10 {
		t.Fatalf("cursor after two passes: %+v", state)
	}
}

func TestPerKeyRepairBeforeReturning(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	e.projectLocally(25, putOp("untouched", 24, 25))
	e.projectLocally(30, putOp("k", 29, 30))
	e.seedOp(20, putOp("k", 19, 20))
	e.seedOp(25, putOp("untouched", 24, 25))
	e.seedOp(30, putOp("k", 29, 30))

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, repaired := e.repairs.counts(); !reflect.DeepEqual(repaired, []string{"k"}) {
		t.Fatalf("repaired keys = %v, want [k]", repaired)
	}
	if got := e.livePut("k"); got != 30 {
		t.Fatalf("k put = %d, want 30", got)
	}
	if got := e.livePut("untouched"); got != 25 {
		t.Fatalf("untouched put = %d, want 25", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM projection_operations WHERE channel_id = ? AND msg_id = 25`, bucketChannel); got != 1 {
		t.Fatalf("operation outcome of the other key was disturbed: %d rows", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ? AND msg_id IN (19, 20) AND reason = 'superseded'`, bucketChannel); got != 2 {
		t.Fatalf("superseded cleanup rows = %d, want 2", got)
	}
	if state := e.state(); state.NeedsProjectionRebuild || state.LastSyncedMsg != 30 {
		t.Fatalf("state after sync: %+v", state)
	}
}

func TestOwnWritesDoNotTriggerRepair(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	e.projectLocally(30, putOp("k", 29, 30))
	e.projectLocally(20, putOp("k", 19, 20))
	e.seedOp(20, putOp("k", 19, 20))
	e.seedOp(30, putOp("k", 29, 30))

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if checks, repaired := e.repairs.counts(); checks != 0 || len(repaired) != 0 {
		t.Fatalf("own writes triggered repair: checks=%d repaired=%v", checks, repaired)
	}
	if got := e.livePut("k"); got != 30 {
		t.Fatalf("k put = %d, want 30", got)
	}
	if state := e.state(); state.NeedsProjectionRebuild || state.LastSyncedMsg != 30 || state.Pts != e.networkPts() {
		t.Fatalf("state after sync: %+v", state)
	}
}

func TestPageCommitsOpsWatermarkAndPtsTogether(t *testing.T) {
	cases := []struct {
		name   string
		inject func(e *env)
		clear  func(e *env)
	}{
		{
			name:   "ingest fails on the last op of the page",
			inject: func(e *env) { e.ingest.setFailAt(30) },
			clear:  func(e *env) { e.ingest.setFailAt(0) },
		},
		{
			name:   "cursor write fails after every op was ingested",
			inject: func(e *env) { e.channels.failCursor = errors.New("test: cursor write failed") },
			clear:  func(e *env) { e.channels.failCursor = nil },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			startPts := e.networkPts()
			e.setCursor(5, startPts, true)
			e.seedOp(10, putOp("a", 9, 10))
			e.seedOp(20, putOp("b", 19, 20))
			e.seedOp(30, putOp("c", 29, 30))

			tc.inject(e)
			if err := e.service.Sync(e.ctx, bucketChannel); err == nil {
				t.Fatal("sync succeeded with an injected failure")
			}
			if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?`, bucketChannel); got != 0 {
				t.Fatalf("replay_log rows after failed page = %d, want 0", got)
			}
			if got := e.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?`, bucketChannel); got != 0 {
				t.Fatalf("objects after failed page = %d, want 0", got)
			}
			if state := e.state(); state.Pts != startPts || state.LastSyncedMsg != 5 {
				t.Fatalf("cursor moved without its ops: %+v", state)
			}

			tc.clear(e)
			if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
				t.Fatalf("retry sync: %v", err)
			}
			if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?`, bucketChannel); got != 3 {
				t.Fatalf("replay_log rows after page = %d, want 3", got)
			}
			if state := e.state(); state.Pts != e.networkPts() || state.LastSyncedMsg != 30 {
				t.Fatalf("cursor after page: %+v, want pts %d watermark 30", state, e.networkPts())
			}
		})
	}
}

func TestTooLongFallsBackToScan(t *testing.T) {
	e := newEnv(t)
	pts := e.networkPts()
	e.setCursor(5, pts, true)
	e.scanner.onScan = nil
	e.seedOp(10, putOp("a", 9, 10))
	if err := e.tg.QueueFaults(faketg.MethodGetChannelDifference, faketg.Fault{Kind: faketg.FaultDifferenceTooLong}); err != nil {
		t.Fatalf("queue fault: %v", err)
	}

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if ensure, full := e.scanner.counts(); ensure != 0 || full != 1 {
		t.Fatalf("scanner calls: ensure=%d full=%d, want 0/1", ensure, full)
	}
	if got := e.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?`, bucketChannel); got != 0 {
		t.Fatalf("too-long difference applied %d ops itself", got)
	}
	if state := e.state(); state.Pts != pts || state.LastSyncedMsg != 5 {
		t.Fatalf("difference moved the cursor on TooLong: %+v", state)
	}
}

func TestSyncPagesUntilFinal(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	for id := int64(10); id < 240; id++ {
		e.seedText(id, "plain chatter")
	}
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if calls := e.tg.CallCount(faketg.MethodGetChannelDifference); calls != 3 {
		t.Fatalf("difference calls = %d, want 3", calls)
	}
	if state := e.state(); state.Pts != e.networkPts() || state.LastSyncedMsg != 239 {
		t.Fatalf("state after paging: %+v, want pts %d watermark 239", state, e.networkPts())
	}
}

func TestSyncEscalatesToScanWhenAuthorityIsMissing(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(e *env)
		wantEnsure int
		wantDiff   bool
	}{
		{"no initial sync", func(e *env) { e.setCursor(0, 0, false) }, 1, false},
		{"rebuild pending", func(e *env) {
			e.setCursor(5, e.networkPts(), true)
			e.exec(`UPDATE channels SET needs_projection_rebuild = 1 WHERE channel_id = ?`, bucketChannel)
		}, 1, false},
		{"authority without pts", func(e *env) { e.setCursor(5, 0, true) }, 1, false},
		{"authority and pts", func(e *env) { e.setCursor(5, e.networkPts(), true) }, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.scanner.onScan = nil
			tc.setup(e)
			if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
				t.Fatalf("sync: %v", err)
			}
			ensure, full := e.scanner.counts()
			if ensure != tc.wantEnsure || full != 0 {
				t.Fatalf("scanner calls: ensure=%d full=%d, want %d/0", ensure, full, tc.wantEnsure)
			}
			if diff := e.tg.CallCount(faketg.MethodGetChannelDifference) > 0; diff != tc.wantDiff {
				t.Fatalf("difference used = %v, want %v", diff, tc.wantDiff)
			}
		})
	}
}

func TestSyncHoldsTheChannelGate(t *testing.T) {
	e := newEnv(t)
	e.setCursor(5, e.networkPts(), true)
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !reflect.DeepEqual(e.gate.acquired, []int64{bucketChannel}) || e.gate.released != 1 {
		t.Fatalf("gate acquired %v released %d, want one acquire of %d and one release", e.gate.acquired, e.gate.released, bucketChannel)
	}
}

func TestSyncRefusesUnusableChannels(t *testing.T) {
	cases := []struct {
		name    string
		channel int64
		setup   func(e *env)
		want    error
	}{
		{"unknown channel", 9999, func(*env) {}, syncer.ErrUnknownChannel},
		{"deleted channel", bucketChannel, func(e *env) {
			e.exec(`UPDATE channels SET state = 'deleted' WHERE channel_id = ?`, bucketChannel)
		}, syncer.ErrUnknownChannel},
		{"zero access hash", bucketChannel, func(e *env) {
			e.setCursor(5, e.networkPts(), true)
			e.exec(`UPDATE channels SET access_hash = 0 WHERE channel_id = ?`, bucketChannel)
		}, tgclient.ErrZeroAccessHash},
		{"scan failure", bucketChannel, func(e *env) {
			e.scanner.failEnsureErr = syncer.ErrHistoryNoProgress
		}, syncer.ErrHistoryNoProgress},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			if err := e.service.Sync(e.ctx, tc.channel); !errors.Is(err, tc.want) {
				t.Fatalf("sync error = %v, want %v", err, tc.want)
			}
			if e.gate.released != 1 {
				t.Fatalf("gate released %d times, want 1", e.gate.released)
			}
		})
	}
}

type flakyDeletions struct {
	syncer.DeletionStore
	bad int64
}

func (f flakyDeletions) RecordDeletedTx(ctx context.Context, tx projection.Tx, channelID int64, msgIDs []int64, source syncer.DeletionSource, recordedAt int64) ([]string, error) {
	for _, id := range msgIDs {
		if id == f.bad {
			return nil, fmt.Errorf("test: cannot record message %d", id)
		}
	}
	return f.DeletionStore.RecordDeletedTx(ctx, tx, channelID, msgIDs, source, recordedAt)
}

func TestDeletionFailureIsLoggedNotFatal(t *testing.T) {
	e := newEnv(t)
	e.deletions = flakyDeletions{DeletionStore: e.channels, bad: 20}
	e.build()
	e.setCursor(5, e.networkPts(), true)
	for _, id := range []int64{10, 20, 30} {
		e.seedOp(id, putOp(fmt.Sprintf("k%d", id), id-1, int(id)))
	}
	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if _, err := e.net.DeleteOutOfBand(bucketChannel, 10, 20, 30); err != nil {
		t.Fatalf("delete out of band: %v", err)
	}

	if err := e.service.Sync(e.ctx, bucketChannel); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := e.count(`SELECT COUNT(*) FROM deleted_messages WHERE channel_id = ? AND msg_id IN (10, 30)`, bucketChannel); got != 2 {
		t.Fatalf("recorded deletions = %d, want 2", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM deleted_messages WHERE channel_id = ? AND msg_id = 20`, bucketChannel); got != 0 {
		t.Fatalf("message 20 recorded despite its failure")
	}
	if state := e.state(); state.Pts != e.networkPts() {
		t.Fatalf("pts = %d, want %d: a deletion failure must not hold the cursor back", state.Pts, e.networkPts())
	}
	if !strings.Contains(e.logs.String(), "recording deleted message failed") {
		t.Fatalf("individual deletion failure was not logged: %s", e.logs.String())
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{"gate", func(d *Dependencies) { d.Gate = nil }},
		{"channels", func(d *Dependencies) { d.Channels = nil }},
		{"cursors", func(d *Dependencies) { d.Cursors = nil }},
		{"deletions", func(d *Dependencies) { d.Deletions = nil }},
		{"scanner", func(d *Dependencies) { d.Scanner = nil }},
		{"ingester", func(d *Dependencies) { d.Ingester = nil }},
		{"differences", func(d *Dependencies) { d.Differences = nil }},
		{"writer", func(d *Dependencies) { d.Writer = nil }},
		{"sleeper", func(d *Dependencies) { d.Sleeper = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := e.dependencies()
			tc.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want missing dependency", err)
			}
		})
	}
}
