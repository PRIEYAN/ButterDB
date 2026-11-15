package remove

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

type batchRecorder struct {
	mu      sync.Mutex
	inner   MessageDeleter
	batches []int
	failing map[int]error
	calls   int
}

func (b *batchRecorder) DeleteMessages(ctx context.Context, peer tgclient.InputPeer, ids []int64) error {
	b.mu.Lock()
	b.calls++
	b.batches = append(b.batches, len(ids))
	failure := b.failing[b.calls]
	b.mu.Unlock()
	if failure != nil {
		return failure
	}
	return b.inner.DeleteMessages(ctx, peer, ids)
}

func TestBatchesOfHundred(t *testing.T) {
	cases := []struct {
		name  string
		total int
		want  []int
	}{
		{"nothing queued", 0, nil},
		{"one short batch", 7, []int{7}},
		{"exactly one batch", 100, []int{100}},
		{"one over", 101, []int{100, 1}},
		{"two and a half", 250, []int{100, 100, 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ids := e.queuePad(tc.total)
			rec := &batchRecorder{inner: e.client}
			if err := e.newDrainer(e.queue, rec).DrainChannel(e.ctx, e.peer.ChannelID); err != nil {
				t.Fatalf("DrainChannel: %v", err)
			}
			if !slices.Equal(rec.batches, tc.want) {
				t.Fatalf("batch sizes = %v, want %v", rec.batches, tc.want)
			}
			if e.pending() != 0 || e.deletedByCleanup() != tc.total {
				t.Fatalf("pending %d, deleted %d, want 0 and %d", e.pending(), e.deletedByCleanup(), tc.total)
			}
			for _, id := range ids {
				if e.channelHas(id) {
					t.Fatalf("message %d not deleted", id)
				}
			}
		})
	}
}

func TestBatchResumeAfterFailedBatch(t *testing.T) {
	e := newEnv(t)
	ids := e.queuePad(250)
	boom := errors.New("rpc timeout")
	rec := &batchRecorder{inner: e.client, failing: map[int]error{2: boom}}
	drainer := e.newDrainer(e.queue, rec)
	err := drainer.DrainChannel(e.ctx, e.peer.ChannelID)
	if !errors.Is(err, boom) {
		t.Fatalf("DrainChannel error = %v, want the first batch error", err)
	}
	if !slices.Equal(rec.batches, []int{100, 100, 50}) {
		t.Fatalf("a failed batch must not stop later batches: %v", rec.batches)
	}
	if e.pending() != 100 || e.deletedByCleanup() != 150 {
		t.Fatalf("pending %d deleted %d, want 100 and 150", e.pending(), e.deletedByCleanup())
	}
	failedIDs := ids[100:200]
	var stillThere []int64
	for _, id := range ids {
		if e.channelHas(id) {
			stillThere = append(stillThere, id)
		}
	}
	if !slices.Equal(stillThere, failedIDs) {
		t.Fatalf("messages left = %d, want exactly the failed batch", len(stillThere))
	}
	if got := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE attempts = 1 AND last_error LIKE '%rpc timeout%'`); got != 100 {
		t.Fatalf("failed rows with recorded error = %d, want 100", got)
	}

	rec.failing = nil
	e.queue.newPass()
	if err := drainer.DrainChannel(e.ctx, e.peer.ChannelID); err != nil {
		t.Fatalf("second DrainChannel: %v", err)
	}
	if e.pending() != 0 || e.deletedByCleanup() != 250 {
		t.Fatalf("after resume pending %d deleted %d", e.pending(), e.deletedByCleanup())
	}
}

func TestDrainReturnsFirstErrorOnly(t *testing.T) {
	e := newEnv(t)
	e.queuePad(300)
	first, second := errors.New("first failure"), errors.New("second failure")
	rec := &batchRecorder{inner: e.client, failing: map[int]error{1: first, 2: second}}
	err := e.newDrainer(e.queue, rec).DrainChannel(e.ctx, e.peer.ChannelID)
	if !errors.Is(err, first) || errors.Is(err, second) {
		t.Fatalf("DrainChannel error = %v, want only the first failure", err)
	}
	if e.deletedByCleanup() != 100 || e.pending() != 200 {
		t.Fatalf("deleted %d pending %d", e.deletedByCleanup(), e.pending())
	}
}

func TestDrainStopsWhenFailedBatchIsClaimedAgain(t *testing.T) {
	e := newEnv(t)
	e.queuePad(10)
	boom := errors.New("always failing")
	rec := &batchRecorder{inner: e.client, failing: map[int]error{1: boom, 2: boom, 3: boom, 4: boom}}
	err := e.newDrainer(&sqlQueue{db: e.db}, rec).DrainChannel(e.ctx, e.peer.ChannelID)
	if !errors.Is(err, boom) {
		t.Fatalf("DrainChannel error = %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("delete calls = %d, want 1: the failed batch must not be retried within one pass", rec.calls)
	}
	if e.pending() != 10 {
		t.Fatalf("pending = %d, want 10", e.pending())
	}
}

func TestDrainRealFaketgPartialDelete(t *testing.T) {
	e := newEnv(t)
	ids := e.queuePad(40)
	if err := e.client.FaultOnCall(faketg.MethodDeleteMessages, 1, faketg.Fault{Kind: faketg.FaultPartialDelete, Count: 15}); err != nil {
		t.Fatalf("fault: %v", err)
	}
	if err := e.newDrainer(e.queue, e.client).DrainChannel(e.ctx, e.peer.ChannelID); err == nil {
		t.Fatalf("DrainChannel hid a partial delete failure")
	}
	if e.pending() != 40 {
		t.Fatalf("pending = %d: a batch reported as failed keeps every id queued", e.pending())
	}
	e.queue.newPass()
	if err := e.newDrainer(e.queue, e.client).DrainChannel(e.ctx, e.peer.ChannelID); err != nil {
		t.Fatalf("redrain: %v", err)
	}
	if e.pending() != 0 {
		t.Fatalf("pending after redrain = %d", e.pending())
	}
	for _, id := range ids {
		if e.channelHas(id) {
			t.Fatalf("message %d survived", id)
		}
	}
}

func TestDrainDeferFailureIsReported(t *testing.T) {
	e := newEnv(t)
	e.queuePad(3)
	queue := &sqlQueue{db: e.db, failDefs: errors.New("queue unavailable")}
	boom := errors.New("delete failed")
	rec := &batchRecorder{inner: e.client, failing: map[int]error{1: boom}}
	err := e.newDrainer(queue, rec).DrainChannel(e.ctx, e.peer.ChannelID)
	if !errors.Is(err, boom) {
		t.Fatalf("DrainChannel error = %v, want the delete failure first", err)
	}
}

func TestDrainCanceledContext(t *testing.T) {
	e := newEnv(t)
	e.queuePad(3)
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	if err := e.newDrainer(e.queue, e.client).DrainChannel(ctx, e.peer.ChannelID); !errors.Is(err, context.Canceled) {
		t.Fatalf("DrainChannel error = %v, want context.Canceled", err)
	}
	if e.pending() != 3 {
		t.Fatalf("canceled drain removed rows")
	}
}

func TestDrainUnresolvablePeer(t *testing.T) {
	e := newEnv(t)
	e.queuePad(2)
	err := e.newDrainer(e.queue, e.client).DrainChannel(e.ctx, 424242)
	if err == nil {
		t.Fatalf("DrainChannel accepted an unknown channel")
	}
	if e.pending() != 2 {
		t.Fatalf("pending = %d", e.pending())
	}
}

func TestLastErrorIsTruncated(t *testing.T) {
	long := strings.Repeat("é", 400)
	got := truncate(long)
	if len(got) > maxLastErrorBytes || !strings.HasPrefix(long, got) {
		t.Fatalf("truncate produced %d bytes", len(got))
	}
	if truncate("short") != "short" {
		t.Fatalf("truncate changed a short text")
	}
}

func TestNewDrainerRejectsMissingDependencies(t *testing.T) {
	e := newEnv(t)
	base := DrainerDependencies{Queue: e.queue, Peers: peerAdapter{client: e.client}, Messages: e.client, Clock: e.clock, Logger: e.deps(nil).Logger}
	mutations := map[string]func(d *DrainerDependencies){
		"queue":    func(d *DrainerDependencies) { d.Queue = nil },
		"peers":    func(d *DrainerDependencies) { d.Peers = nil },
		"messages": func(d *DrainerDependencies) { d.Messages = nil },
		"clock":    func(d *DrainerDependencies) { d.Clock = nil },
		"logger":   func(d *DrainerDependencies) { d.Logger = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			deps := base
			mutate(&deps)
			if _, err := NewDrainer(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewDrainer error = %v", err)
			}
		})
	}
	if _, err := NewDrainer(base); err != nil {
		t.Fatalf("NewDrainer: %v", err)
	}
}

func TestDeferredAttemptIsMilliseconds(t *testing.T) {
	e := newEnv(t)
	e.queuePad(3)
	boom := errors.New("delete failed")
	rec := &batchRecorder{inner: e.client, failing: map[int]error{1: boom}}
	if err := e.newDrainer(e.queue, rec).DrainChannel(e.ctx, e.peer.ChannelID); !errors.Is(err, boom) {
		t.Fatalf("DrainChannel error = %v", err)
	}
	nowMilli := e.clock.Now().UnixMilli()
	if got := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE next_attempt_at = ?1`, nowMilli); got != 3 {
		t.Fatalf("rows deferred to the current time in milliseconds = %d, want 3", got)
	}
	e.queue.newPass()
	if err := e.newDrainer(e.queue, e.client).DrainChannel(e.ctx, e.peer.ChannelID); err != nil {
		t.Fatalf("redrain: %v", err)
	}
	if got := e.count(`SELECT COUNT(*) FROM deleted_messages WHERE recorded_at = ?1`, nowMilli); got != 3 {
		t.Fatalf("recorded_at in milliseconds = %d rows, want 3", got)
	}
}
