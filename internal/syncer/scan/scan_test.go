package scan

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

type callRecorder struct {
	HistoryReader
	channels []int64
	onCall   func(call int)
	calls    int
}

func (r *callRecorder) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	r.calls++
	r.channels = append(r.channels, peer.ChannelID)
	page, err := r.HistoryReader.GetHistory(ctx, peer, offsetID, limit)
	if r.onCall != nil {
		r.onCall(r.calls)
	}
	return page, err
}

func firstCallOrder(channels []int64) []int64 {
	var order []int64
	seen := map[int64]bool{}
	for _, id := range channels {
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	return order
}

func TestRegistryScannedFirst(t *testing.T) {
	var recorder *callRecorder
	e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		recorder = &callRecorder{HistoryReader: base}
		return recorder
	}))
	e.addChannel(bucketID, syncer.KindBucket)
	if _, err := e.net.SeedChannel(5001, "discovered", "", e.client); err != nil {
		t.Fatal(err)
	}
	e.seed(registryID, bucketCreateText(t, 1))
	e.seed(bucketID, "plain chat message")

	if err := e.scanner.EnsureAllAuthoritative(context.Background()); err != nil {
		t.Fatalf("EnsureAllAuthoritative() error = %v", err)
	}
	order := firstCallOrder(recorder.channels)
	want := []int64{registryID, bucketID, 5001}
	if len(order) != len(want) {
		t.Fatalf("scan order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("scan order = %v, want %v", order, want)
		}
	}
	for _, id := range want {
		if !e.state(id).InitialSyncDone {
			t.Fatalf("channel %d has no authority after the pass", id)
		}
	}
}

func TestRegistryFailureSkipsBuckets(t *testing.T) {
	var recorder *callRecorder
	rejected := errors.New("CHANNEL_PRIVATE")
	e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		recorder = &callRecorder{HistoryReader: base}
		return recorder
	}))
	e.addChannel(bucketID, syncer.KindBucket)
	if err := e.client.QueueFaults(faketg.MethodGetHistory, faketg.Fault{Kind: faketg.FaultError, Err: rejected}); err != nil {
		t.Fatal(err)
	}

	err := e.scanner.EnsureAllAuthoritative(context.Background())
	if !errors.Is(err, rejected) {
		t.Fatalf("EnsureAllAuthoritative() error = %v, want the registry failure", err)
	}
	for _, id := range recorder.channels {
		if id != registryID {
			t.Fatalf("bucket channel %d was read after the registry failed", id)
		}
	}
	if e.state(bucketID).InitialSyncDone {
		t.Fatal("bucket earned authority although the registry did not")
	}
}

func TestEnsureAllWithoutRegistry(t *testing.T) {
	e := newEnv(t)
	e.exec(`DELETE FROM channels`)
	if err := e.scanner.EnsureAllAuthoritative(context.Background()); err != nil {
		t.Fatalf("no channels at all: %v", err)
	}
	e.addChannelRowOnly(bucketID, syncer.KindBucket)
	err := e.scanner.EnsureAllAuthoritative(context.Background())
	if !errors.Is(err, syncer.ErrUnknownChannel) {
		t.Fatalf("buckets without a registry: error = %v, want ErrUnknownChannel", err)
	}
}

func TestScanEarnsAuthority(t *testing.T) {
	e := newEnv(t)
	e.seed(registryID, bucketCreateText(t, 1))
	e.seed(registryID, "chat noise without a header")
	last := e.seed(registryID, bucketConfigText(t, 1, "public-read", 2))
	pts, err := e.net.Pts(registryID)
	if err != nil {
		t.Fatal(err)
	}
	if before := e.state(registryID); before.InitialSyncDone || before.LastSyncedMsg != 0 {
		t.Fatalf("channel state before the scan = %#v", before)
	}

	if err := e.scanner.FullScan(context.Background(), registryID); err != nil {
		t.Fatalf("FullScan() error = %v", err)
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != last || state.Pts != pts {
		t.Fatalf("state after scan = %#v, want authority through %d with pts %d", state, last, pts)
	}
	if policy, ok := e.bucketPolicy(5001); !ok || policy != "public-read" {
		t.Fatalf("bucket policy = %q, found %v, want the scanned config applied", policy, ok)
	}
	if rows := e.count(`SELECT COUNT(*) FROM initial_scan_spool`); rows != 0 {
		t.Fatalf("spool kept %d rows after a successful scan", rows)
	}
}

func TestScanOfEmptyChannelEarnsAuthority(t *testing.T) {
	e := newEnv(t)
	if err := e.scanner.FullScan(context.Background(), registryID); err != nil {
		t.Fatalf("FullScan() error = %v", err)
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != 0 {
		t.Fatalf("state after an empty scan = %#v", state)
	}
}

func TestCancelBeforeApplyLeavesNoPartialState(t *testing.T) {
	cancelOnIngest := func(cancel context.CancelFunc) func(base syncer.OpIngester) syncer.OpIngester {
		return func(base syncer.OpIngester) syncer.OpIngester {
			return &cancellingIngester{OpIngester: base, cancel: cancel, after: 1}
		}
	}
	for _, test := range []struct {
		name string
		arm  func(cancel context.CancelFunc) envOption
	}{
		{name: "after spooling", arm: func(cancel context.CancelFunc) envOption {
			return withHistory(func(base HistoryReader) HistoryReader {
				return &callRecorder{HistoryReader: base, onCall: func(call int) {
					if call == 3 {
						cancel()
					}
				}}
			})
		}},
		{name: "during apply", arm: func(cancel context.CancelFunc) envOption {
			return withIngester(cancelOnIngest(cancel))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := newEnv(t, test.arm(cancel))
			e.seed(registryID, bucketCreateText(t, 1))
			e.seed(registryID, bucketCreateText(t, 2))
			e.seed(registryID, bucketCreateText(t, 3))

			err := e.scanner.FullScan(ctx, registryID)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("FullScan() error = %v, want cancellation", err)
			}
			if !e.channelEmpty() {
				t.Fatal("a cancelled scan left projection rows behind")
			}
			if state := e.state(registryID); state.InitialSyncDone || state.LastSyncedMsg != 0 {
				t.Fatalf("a cancelled scan moved the cursor: %#v", state)
			}
			if n := e.count(`SELECT COUNT(*) FROM buckets`); n != 0 {
				t.Fatalf("a cancelled scan applied %d buckets", n)
			}
		})
	}
}

type cancellingIngester struct {
	syncer.OpIngester
	cancel context.CancelFunc
	after  int
	seen   int
}

func (c *cancellingIngester) IngestTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error) {
	out, err := c.OpIngester.IngestTx(ctx, tx, obs)
	c.seen++
	if c.seen == c.after {
		c.cancel()
	}
	return out, err
}

func TestFullScanClearsStaleSpoolRows(t *testing.T) {
	e := newEnv(t)
	stale := tgclient.HistoryMessage{MsgID: 999, Text: bucketCreateText(t, 9), Date: 1}
	if err := e.stores.SpoolPage(context.Background(), registryID, []tgclient.HistoryMessage{stale}); err != nil {
		t.Fatal(err)
	}
	e.seed(registryID, bucketCreateText(t, 1))

	if err := e.scanner.FullScan(context.Background(), registryID); err != nil {
		t.Fatalf("FullScan() error = %v", err)
	}
	if _, ok := e.bucketPolicy(5009); ok {
		t.Fatal("a stale spool row from an abandoned scan was applied")
	}
	if _, ok := e.bucketPolicy(5001); !ok {
		t.Fatal("the scanned bucket is missing")
	}
}

func TestIncrementalReadsOnlyAboveTheWatermark(t *testing.T) {
	var recorder *callRecorder
	e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		recorder = &callRecorder{HistoryReader: base}
		return recorder
	}))
	for n := 1; n <= 3; n++ {
		e.seed(registryID, bucketCreateText(t, n))
	}
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatal(err)
	}
	last := e.seed(registryID, bucketCreateText(t, 4))
	recorder.calls = 0
	recorder.channels = nil

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("incremental refresh: %v", err)
	}
	if recorder.calls != 2 {
		t.Fatalf("history reads = %d, want one page and the terminating page", recorder.calls)
	}
	state := e.state(registryID)
	if state.LastSyncedMsg != last {
		t.Fatalf("watermark = %d, want %d", state.LastSyncedMsg, last)
	}
	if _, ok := e.bucketPolicy(5004); !ok {
		t.Fatal("the new op was not projected")
	}
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("idempotent refresh: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM replay_log`); n != 4 {
		t.Fatalf("replay_log rows = %d, want 4", n)
	}
}

func TestIncrementalEscalatesWhenRebuildPending(t *testing.T) {
	e := newEnv(t)
	e.seed(registryID, bucketCreateText(t, 1))
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE channels SET needs_projection_rebuild = 1, last_synced_msg = 999999 WHERE channel_id = ?`, registryID)
	e.seed(registryID, bucketCreateText(t, 2))

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("escalated scan: %v", err)
	}
	state := e.state(registryID)
	if state.NeedsProjectionRebuild || state.LastSyncedMsg == 999999 {
		t.Fatalf("state after escalation = %#v, want a full scan from message zero", state)
	}
	if _, ok := e.bucketPolicy(5002); !ok {
		t.Fatal("the full scan did not read past the bogus watermark")
	}
}

func TestInitialSyncEmptyChannelMarksAuthority(t *testing.T) {
	e := newEnv(t)
	e.seed(registryID, "chat noise")
	pts, err := e.net.Pts(registryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.scanner.InitialSyncEmptyChannel(context.Background(), registryID); err != nil {
		t.Fatalf("InitialSyncEmptyChannel() error = %v", err)
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != 0 || state.Pts != pts {
		t.Fatalf("state = %#v, want authority at watermark 0 with sampled pts %d", state, pts)
	}
}

func TestInitialSyncEmptyChannelRefusesWhenProjectionHasRows(t *testing.T) {
	e := newEnv(t)
	e.project(registryID, 5, bucketCreateText(t, 1))
	err := e.scanner.InitialSyncEmptyChannel(context.Background(), registryID)
	if !errors.Is(err, syncer.ErrChannelNotEmpty) {
		t.Fatalf("InitialSyncEmptyChannel() error = %v, want ErrChannelNotEmpty", err)
	}
	if e.state(registryID).InitialSyncDone {
		t.Fatal("a refused initial sync still marked authority")
	}
}

func TestUnknownChannelIsReported(t *testing.T) {
	e := newEnv(t)
	for name, call := range map[string]func(int64) error{
		"ensure":  func(id int64) error { return e.scanner.EnsureAuthoritative(context.Background(), id) },
		"full":    func(id int64) error { return e.scanner.FullScan(context.Background(), id) },
		"initial": func(id int64) error { return e.scanner.InitialSyncEmptyChannel(context.Background(), id) },
	} {
		if err := call(4242); !errors.Is(err, syncer.ErrUnknownChannel) {
			t.Fatalf("%s: error = %v, want ErrUnknownChannel", name, err)
		}
	}
}

func TestNewScannerRejectsNilDependencies(t *testing.T) {
	e := newEnv(t)
	complete := func() Dependencies {
		return Dependencies{
			Channels: e.stores, Cursors: e.stores, Spool: e.stores, Ingester: e.ingest, Rebuilder: e.scanner.rebuilder,
			Writer: e.db, Peers: e.scanner.peers, History: e.client, Gate: e.gate, Sleeper: e.sleeper,
			Clock: clock.System{}, Random: io.Reader(zeroReader{}), Logger: discardLogger(),
		}
	}
	if _, err := NewScanner(complete()); err != nil {
		t.Fatalf("complete dependencies: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"channels", func(d *Dependencies) { d.Channels = nil }},
		{"cursors", func(d *Dependencies) { d.Cursors = nil }},
		{"spool", func(d *Dependencies) { d.Spool = nil }},
		{"ingester", func(d *Dependencies) { d.Ingester = nil }},
		{"rebuilder", func(d *Dependencies) { d.Rebuilder = nil }},
		{"writer", func(d *Dependencies) { d.Writer = nil }},
		{"peers", func(d *Dependencies) { d.Peers = nil }},
		{"history", func(d *Dependencies) { d.History = nil }},
		{"gate", func(d *Dependencies) { d.Gate = nil }},
		{"sleeper", func(d *Dependencies) { d.Sleeper = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"random", func(d *Dependencies) { d.Random = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := complete()
			test.mutate(&deps)
			if _, err := NewScanner(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewScanner() error = %v, want ErrMissingDependency", err)
			}
		})
	}
}
