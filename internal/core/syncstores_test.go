package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/channelrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func newStores(t *testing.T, w *registryWorld) *channelStores {
	t.Helper()
	repo, err := channelrepo.New(w.projectionDB)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := newChannelStores(repo)
	if err != nil {
		t.Fatal(err)
	}
	return stores
}

func TestChannelStoresMapStatesAndMissingChannels(t *testing.T) {
	w := newRegistryWorld(t)
	stores := newStores(t, w)
	ctx := context.Background()
	row := syncer.ChannelState{ChannelID: 700, AccessHash: 11, Kind: syncer.KindBucket, Title: "butterDB bucket photos"}
	if err := stores.UpsertChannel(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, found, err := stores.Channel(ctx, 700)
	if err != nil || !found || got.Kind != syncer.KindBucket || got.AccessHash != 11 || got.Title != row.Title || got.InitialSyncDone {
		t.Fatalf("channel = %+v, %v, %v", got, found, err)
	}
	if _, found, err := stores.Channel(ctx, 701); err != nil || found {
		t.Fatalf("missing channel = %v, %v; want found=false", found, err)
	}
	if err := stores.SetNeedsProjectionRebuild(ctx, 701, true); !errors.Is(err, syncer.ErrUnknownChannel) {
		t.Fatalf("flag on a missing channel = %v, want ErrUnknownChannel", err)
	}
	if err := stores.SetLastReconciledAt(ctx, 701, 5); !errors.Is(err, syncer.ErrUnknownChannel) {
		t.Fatalf("reconciled time on a missing channel = %v, want ErrUnknownChannel", err)
	}
	all, err := stores.Channels(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("channels = %v, %v", all, err)
	}
}

func TestChannelStoresSpoolRoundTripClearsPlaceholder(t *testing.T) {
	w := newRegistryWorld(t)
	stores := newStores(t, w)
	ctx := context.Background()
	if err := stores.UpsertChannel(ctx, syncer.ChannelState{ChannelID: 700, AccessHash: 11, Kind: syncer.KindBucket}); err != nil {
		t.Fatal(err)
	}
	msgs := []tgclient.HistoryMessage{
		{MsgID: 3, Date: 30, FromID: 7, Text: "TDX1|t=x", HasMedia: true, MediaSize: 9, DocumentName: "a", Placeholder: true},
		{MsgID: 4, Date: 31, Text: "plain"},
	}
	if err := stores.SpoolPage(ctx, 700, msgs); err != nil {
		t.Fatalf("spool: %v", err)
	}
	var page []tgclient.HistoryMessage
	err := w.projectionDB.Read(ctx, func(tx *sql.Tx) error {
		var readErr error
		page, readErr = stores.SpooledPageTx(ctx, tx, 700, 0, 10)
		return readErr
	})
	if err != nil || len(page) != 2 {
		t.Fatalf("spooled page = %v, %v", page, err)
	}
	want := msgs[0]
	want.Placeholder = false
	if page[0] != want {
		t.Fatalf("first spooled = %+v, want %+v", page[0], want)
	}
	if err := stores.ClearSpool(ctx, 700); err != nil {
		t.Fatal(err)
	}
	if got := w.count(`SELECT COUNT(*) FROM initial_scan_spool`); got != 0 {
		t.Fatalf("spool rows after clear = %d", got)
	}
}

func TestChannelStoresCursorsAndDeletionsRunInCallerTransaction(t *testing.T) {
	w := newRegistryWorld(t)
	stores := newStores(t, w)
	ctx := context.Background()
	if err := stores.UpsertChannel(ctx, syncer.ChannelState{ChannelID: 700, AccessHash: 11, Kind: syncer.KindBucket}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	err := w.projectionDB.Write(ctx, func(tx *sql.Tx) error {
		if err := stores.MarkInitialSyncDoneTx(ctx, tx, 700, 9, 4); err != nil {
			return err
		}
		removed, err := stores.RecordDeletedTx(ctx, tx, 700, []int64{5}, syncer.DeletedExternal, 100)
		if err != nil || len(removed) != 0 {
			t.Errorf("RecordDeletedTx = %v, %v", removed, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("write = %v", err)
	}
	state, _, _ := stores.Channel(ctx, 700)
	if state.InitialSyncDone || state.Pts != 0 || w.count(`SELECT COUNT(*) FROM deleted_messages`) != 0 {
		t.Fatalf("a rolled back transaction left state behind: %+v", state)
	}
	err = w.projectionDB.Write(ctx, func(tx *sql.Tx) error {
		if err := stores.SetSyncCursorTx(ctx, tx, 700, 9, 4); err != nil {
			return err
		}
		return stores.MarkInitialSyncDoneTx(ctx, tx, 700, 9, 4)
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ = stores.Channel(ctx, 700)
	if !state.InitialSyncDone || state.Pts != 4 || state.LastSyncedMsg != 9 {
		t.Fatalf("committed state = %+v", state)
	}
}

type resolverStub struct {
	peer  tgclient.InputPeer
	err   error
	calls int
}

func (r *resolverStub) ResolveChannel(context.Context, int64) (tgclient.InputPeer, error) {
	r.calls++
	return r.peer, r.err
}

func TestChannelPeersResolvePeer(t *testing.T) {
	cases := []struct {
		name      string
		hash      int64
		resolved  tgclient.InputPeer
		resolveBy error
		channel   int64
		want      tgclient.InputPeer
		wantErr   error
		wantCalls int
	}{
		{name: "stored hash is used", hash: 11, channel: 700, want: tgclient.InputPeer{ChannelID: 700, AccessHash: 11}},
		{name: "zero hash is resolved and saved", channel: 700, resolved: tgclient.InputPeer{ChannelID: 700, AccessHash: 22}, want: tgclient.InputPeer{ChannelID: 700, AccessHash: 22}, wantCalls: 1},
		{name: "resolved zero hash is refused", channel: 700, resolved: tgclient.InputPeer{ChannelID: 700}, wantErr: tgclient.ErrZeroAccessHash, wantCalls: 1},
		{name: "resolved other channel is refused", channel: 700, resolved: tgclient.InputPeer{ChannelID: 9, AccessHash: 3}, wantErr: tgclient.ErrChannelUnavailable, wantCalls: 1},
		{name: "unknown channel", channel: 701, wantErr: syncer.ErrUnknownChannel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRegistryWorld(t)
			stores := newStores(t, w)
			ctx := context.Background()
			err := w.projectionDB.Write(ctx, func(tx *sql.Tx) error {
				_, execErr := tx.ExecContext(ctx, `INSERT INTO channels (channel_id, kind, access_hash) VALUES (700, 'bucket', ?1)`, tc.hash)
				return execErr
			})
			if err != nil {
				t.Fatal(err)
			}
			resolver := &resolverStub{peer: tc.resolved, err: tc.resolveBy}
			peers := &channelPeers{channels: stores, resolver: resolver}
			got, err := peers.ResolvePeer(ctx, tc.channel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("peer = %+v, %v; want %+v", got, err, tc.want)
			}
			if resolver.calls != tc.wantCalls {
				t.Fatalf("resolver calls = %d, want %d", resolver.calls, tc.wantCalls)
			}
			if tc.wantErr == nil && tc.hash == 0 {
				state, _, _ := stores.Channel(ctx, 700)
				if state.AccessHash != tc.want.AccessHash {
					t.Fatalf("stored hash = %d, want %d", state.AccessHash, tc.want.AccessHash)
				}
			}
		})
	}
}

func TestEncryptionKeyMinterProducesProjectableConfig(t *testing.T) {
	w := newRegistryWorld(t)
	minter := &encryptionKeyMinter{ring: w.ring, random: randomReader{}}
	op, err := minter.MintEncryptionConfig(context.Background(), 700)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	op.JobID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	op.Timestamp = 1763000000
	text, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	parsed, err := wire.Parse(text)
	if err != nil || parsed.KEKFingerprint != w.ring.FingerprintString() || parsed.BucketChannelID != 700 {
		t.Fatalf("parsed = %+v, %v", parsed, err)
	}
	key, err := w.ring.UnwrapDEK(parsed.WrappedKey, parsed.KeyCheck, 700, parsed.KeyID)
	if err != nil {
		t.Fatalf("minted key does not unwrap: %v", err)
	}
	key.Clear()
}

type randomReader struct{}

func (randomReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i*7 + 1)
	}
	return len(p), nil
}

type liveStub struct {
	refs []objectrepo.MessageRefs
	err  error
}

func (l liveStub) LiveMessageIDs(context.Context, int64) ([]objectrepo.MessageRefs, error) {
	return l.refs, l.err
}

func TestLiveObjectsMapsReferences(t *testing.T) {
	live, err := newLiveObjects(liveStub{refs: []objectrepo.MessageRefs{{Key: "a", PutMsgID: 5, BackingMsgIDs: []int64{5, 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := live.LiveMessageIDs(context.Background(), 700)
	if err != nil || len(got) != 1 || got[0].Key != "a" || got[0].PutMsgID != 5 || len(got[0].BackingMsgIDs) != 2 {
		t.Fatalf("refs = %+v, %v", got, err)
	}
	if _, err := newLiveObjects(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil source = %v, want ErrMissingDependency", err)
	}
}

func TestNewSyncWiresAndRejectsMissingDependencies(t *testing.T) {
	w := newRegistryWorld(t)
	state := newStateFile(t, w)
	complete := SyncDependencies{
		Database: w.projectionDB, Projection: w.projection, Telegram: w.client, Sender: w.client,
		Signals: conn.NewActivity(0), Ring: w.ring, State: state, ActorUserID: registryWorldUser,
		DeploymentID: "6pzyoxbfwcf6ubwcq5gpvjg5c4", Clock: w.clk, Sleeper: flow.SystemSleeper{}, Random: randomReader{}, Logger: newDiscardLogger(),
	}
	built, err := NewSync(complete)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	if built.Gate == nil || built.Scanner == nil || built.Difference == nil || built.Coordinator == nil ||
		built.Reconciler == nil || built.Orphans == nil || built.Buckets == nil || built.Registry == nil {
		t.Fatalf("sync has a nil member: %+v", built)
	}
	cases := []struct {
		name   string
		mutate func(*SyncDependencies)
	}{
		{"nil database", func(d *SyncDependencies) { d.Database = nil }},
		{"nil projection", func(d *SyncDependencies) { d.Projection = nil }},
		{"nil telegram", func(d *SyncDependencies) { d.Telegram = nil }},
		{"nil sender", func(d *SyncDependencies) { d.Sender = nil }},
		{"nil signals", func(d *SyncDependencies) { d.Signals = nil }},
		{"nil ring", func(d *SyncDependencies) { d.Ring = nil }},
		{"nil state", func(d *SyncDependencies) { d.State = nil }},
		{"empty deployment", func(d *SyncDependencies) { d.DeploymentID = "" }},
		{"nil clock", func(d *SyncDependencies) { d.Clock = nil }},
		{"nil sleeper", func(d *SyncDependencies) { d.Sleeper = nil }},
		{"nil random", func(d *SyncDependencies) { d.Random = nil }},
		{"nil logger", func(d *SyncDependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := complete
			tc.mutate(&deps)
			if got, err := NewSync(deps); got != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewSync = %v, %v; want ErrMissingDependency", got, err)
			}
		})
	}
}
