package scan

import (
	"context"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type shortPagePager struct {
	HistoryReader
	live    []tgclient.HistoryMessage
	pageCap int
}

func (p *shortPagePager) GetHistory(_ context.Context, _ tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	limit = min(limit, p.pageCap)
	page := make([]tgclient.HistoryMessage, 0, limit)
	for index := len(p.live) - 1; index >= 0 && len(page) < limit; index-- {
		msg := p.live[index]
		if offsetID != 0 && msg.MsgID >= offsetID {
			continue
		}
		page = append(page, msg)
	}
	return page, nil
}

func shortPages(live []tgclient.HistoryMessage, pageCap int) envOption {
	return withHistory(func(base HistoryReader) HistoryReader {
		return &shortPagePager{HistoryReader: base, live: live, pageCap: pageCap}
	})
}

func TestEnsureAuthoritativeScansPastPagesThinnedByDeletions(t *testing.T) {
	live := []tgclient.HistoryMessage{{MsgID: 10, Text: bucketCreateText(t, 1)}}
	for id := int64(201); id <= 300; id++ {
		msg := tgclient.HistoryMessage{MsgID: id}
		if id == 260 {
			msg.Text = bucketConfigText(t, 1, "public-read", 2)
		}
		live = append(live, msg)
	}
	e := newEnv(t, shortPages(live, 60))

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	if policy, ok := e.bucketPolicy(5001); !ok || policy != "public-read" {
		t.Fatalf("bucket policy = %q, found %v, want the config near the newest history applied to the oldest bucket", policy, ok)
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != 300 {
		t.Fatalf("channel state = %#v, want authoritative through 300", state)
	}
}

func TestFullScanRepairsOpsBankedAgainstUnreadObjects(t *testing.T) {
	live := []tgclient.HistoryMessage{
		{MsgID: 5, Text: bucketCreateText(t, 1)},
		{MsgID: 9, Text: bucketConfigText(t, 1, "public-read", 2)},
	}
	e := newEnv(t, shortPages(live, 60))
	banked := e.project(registryID, 9, bucketConfigText(t, 1, "public-read", 2))
	if banked.RejectCode == "" {
		t.Fatalf("seeded config outcome = %#v, want a reject because the bucket was never read", banked)
	}
	if _, ok := e.bucketPolicy(5001); ok {
		t.Fatal("the bucket exists before the scan read its creation")
	}
	e.exec(`UPDATE channels SET last_synced_msg = 0, initial_sync_done = 0, needs_projection_rebuild = 1 WHERE channel_id = ?`, registryID)

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	if policy, ok := e.bucketPolicy(5001); !ok || policy != "public-read" {
		t.Fatalf("bucket policy = %q, found %v, want the banked config replayed as public-read", policy, ok)
	}
	state := e.state(registryID)
	if state.NeedsProjectionRebuild {
		t.Fatal("the rebuild flag survived the repair, so every later sync would rescan")
	}
	if !state.InitialSyncDone {
		t.Fatal("the repairing scan did not earn authority")
	}
}

func TestFullScanRebuildsBucketChannelAlone(t *testing.T) {
	e := newEnv(t, shortPages([]tgclient.HistoryMessage{{MsgID: 4, Text: "noise"}}, 60))
	e.addChannel(bucketID, syncer.KindBucket)
	e.exec(`UPDATE channels SET needs_projection_rebuild = 1 WHERE channel_id = ?`, bucketID)

	if err := e.scanner.EnsureAuthoritative(context.Background(), bucketID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	state := e.state(bucketID)
	if state.NeedsProjectionRebuild || !state.InitialSyncDone || state.LastSyncedMsg != 4 {
		t.Fatalf("bucket channel state = %#v, want authority through 4 with the rebuild cleared", state)
	}
}
