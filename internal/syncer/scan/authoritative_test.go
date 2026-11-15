package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestEnsureAuthoritativeReconcilesNonEmptyHistoryAndPersistsMarker(t *testing.T) {
	e := newEnv(t)
	createID := e.seed(registryID, bucketCreateText(t, 1))
	configID := e.seed(registryID, bucketConfigText(t, 1, "public-read", 2))
	e.project(registryID, createID, bucketCreateText(t, 1))
	e.exec(`UPDATE channels SET last_synced_msg = ?, initial_sync_done = 0 WHERE channel_id = ?`, createID, registryID)

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("EnsureAuthoritative() error = %v", err)
	}
	if policy, ok := e.bucketPolicy(5001); !ok || policy != "public-read" {
		t.Fatalf("config msg %d was not reconciled: policy %q found %v", configID, policy, ok)
	}
	state := e.state(registryID)
	if !state.InitialSyncDone || state.LastSyncedMsg != configID {
		t.Fatalf("authoritative channel state = %#v, want marker through %d", state, configID)
	}
}

func TestEnsureAuthoritativeDoesNotPersistMarkerAfterPartialFailure(t *testing.T) {
	e := newEnv(t)
	e.seed(registryID, bucketCreateText(t, 1))
	floods := make([]faketg.Fault, maxFloodWaitRetries+2)
	for i := range floods {
		floods[i] = faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 1}
	}
	if err := e.client.QueueFaults(faketg.MethodGetHistory, floods...); err != nil {
		t.Fatal(err)
	}

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err == nil {
		t.Fatal("EnsureAuthoritative() error = nil, want history failure")
	}
	if e.state(registryID).InitialSyncDone {
		t.Fatal("failed authoritative sync persisted initial_sync_done")
	}
	if _, ok := e.bucketPolicy(5001); ok {
		t.Fatal("failed authoritative sync projected the bucket")
	}
}

func TestEnsureAuthoritativeUsesIncrementalAfterMarker(t *testing.T) {
	e := newEnv(t)
	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("establish empty authority: %v", err)
	}
	e.seed(registryID, bucketCreateText(t, 1))

	if err := e.scanner.EnsureAuthoritative(context.Background(), registryID); err != nil {
		t.Fatalf("refresh authoritative state: %v", err)
	}
	if _, ok := e.bucketPolicy(5001); !ok {
		t.Fatal("incremental refresh did not project the bucket")
	}
}

type repeatingHistory struct {
	HistoryReader
	page  []tgclient.HistoryMessage
	calls int
}

func (r *repeatingHistory) GetHistory(context.Context, tgclient.InputPeer, int64, int) ([]tgclient.HistoryMessage, error) {
	r.calls++
	return append([]tgclient.HistoryMessage(nil), r.page...), nil
}

func TestEnsureAuthoritativeRejectsRepeatedFullHistoryPageWithoutProgress(t *testing.T) {
	page := make([]tgclient.HistoryMessage, defaultPageSize)
	for index := range page {
		page[index] = tgclient.HistoryMessage{MsgID: int64(200 - index)}
	}
	var repeating *repeatingHistory
	e := newEnv(t, withHistory(func(base HistoryReader) HistoryReader {
		repeating = &repeatingHistory{HistoryReader: base, page: page}
		return repeating
	}))

	err := e.scanner.EnsureAuthoritative(context.Background(), registryID)
	if !errors.Is(err, syncer.ErrHistoryNoProgress) {
		t.Fatalf("EnsureAuthoritative() error = %v, want pagination no-progress", err)
	}
	if repeating.calls != 2 {
		t.Fatalf("GetHistory() calls = %d, want 2", repeating.calls)
	}
	if state := e.state(registryID); state.InitialSyncDone || state.LastSyncedMsg != 0 {
		t.Fatalf("partial scan earned authority: %#v", state)
	}
}
