package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	testUser       int64 = 777000123
	otherUser      int64 = 777000999
	testDeployment       = "6pzyoxbfwcf6ubwcq5gpvjg5c4"
	otherDeploy          = "7pzyoxbfwcf6ubwcq5gpvjg5c4"
	testKFP              = "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"
	otherKFP             = "bkf1_00000000000000000000000000000001"
	testJobID            = "00000000-0000-4000-8000-000000000001"
	testTS         int64 = 1763000000
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeWorld struct {
	clock *clock.Fake
	net   *faketg.Network
	alice *faketg.Client
	rand  int64
}

func newWorld(t *testing.T) *fakeWorld {
	t.Helper()
	clk := clock.NewFake(time.Unix(testTS, 0).UTC())
	net, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("new network: %v", err)
	}
	alice, err := net.AddAccount(faketg.AccountConfig{UserID: testUser, FirstName: "Alice", Username: "alice"})
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	return &fakeWorld{clock: clk, net: net, alice: alice}
}

func (w *fakeWorld) channel(t *testing.T, title string) tgclient.InputPeer {
	t.Helper()
	peer, err := w.alice.CreateChannel(context.Background(), title, "")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return peer
}

func (w *fakeWorld) say(t *testing.T, peer tgclient.InputPeer, text string) int64 {
	t.Helper()
	w.rand++
	id, err := w.alice.SendControlIdempotent(context.Background(), tgclient.SendControlRequest{Peer: peer, Text: text, RandomID: w.rand})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return id
}

func (w *fakeWorld) op(t *testing.T, peer tgclient.InputPeer, op wire.Op) int64 {
	t.Helper()
	if op.Timestamp == 0 {
		op.Timestamp = testTS
	}
	text, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return w.say(t, peer, text)
}

func marker(dep, kfp string, uid int64) wire.Op {
	return wire.Op{Type: wire.OpRegistry, DeploymentID: dep, FormatVersion: 1, MinReaderVersion: 1, KEKFingerprint: kfp, PrimaryUserID: uid}
}

func bucketOp(t wire.OpType, name string, channelID int64) wire.Op {
	return wire.Op{
		Type: t, JobID: testJobID, Bucket: name, BucketChannelID: channelID,
		Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600,
	}
}

func newDiscoverer(t *testing.T, w *fakeWorld, id Identity) *Discoverer {
	t.Helper()
	d, err := NewDiscoverer(DiscoveryDependencies{Telegram: w.alice, Identity: id, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new discoverer: %v", err)
	}
	return d
}

func ownIdentity() Identity {
	return Identity{KeyFingerprint: testKFP, UserID: testUser}
}

func TestRediscoveryFindsRegistryCandidate(t *testing.T) {
	w := newWorld(t)
	registry := w.channel(t, "butterDB registry")
	w.channel(t, "my diary")
	noMarker := w.channel(t, "butterDB bucket stray")
	w.say(t, noMarker, "hello")
	w.say(t, w.channel(t, "random"), "TDX1|t=bogus")

	w.clock.Advance(time.Hour)
	markerID := w.op(t, registry, marker(testDeployment, testKFP, testUser))
	w.op(t, registry, bucketOp(wire.OpBucketCreate, "photos", 5001))
	w.op(t, registry, bucketOp(wire.OpBucketCreate, "docs", 5002))
	w.op(t, registry, bucketOp(wire.OpBucketDelete, "docs", 5002))

	got, err := newDiscoverer(t, w, ownIdentity()).Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Peer.ChannelID != registry.ChannelID || c.Peer.AccessHash != registry.AccessHash {
		t.Fatalf("peer = %+v, want %+v", c.Peer, registry)
	}
	if c.Title != "butterDB registry" || c.DeploymentID != testDeployment || c.KeyFingerprint != testKFP || c.UserID != testUser {
		t.Fatalf("identity fields wrong: %+v", c)
	}
	if c.MarkerMsgID != markerID || c.FormatVersion != 1 || c.MinReaderVersion != 1 {
		t.Fatalf("marker fields wrong: %+v", c)
	}
	if c.LiveBuckets != 1 {
		t.Fatalf("live buckets = %d, want 1", c.LiveBuckets)
	}
	if c.CreatedAt != testTS+3600 {
		t.Fatalf("created at = %d, want %d", c.CreatedAt, testTS+3600)
	}
	if !c.Eligible() {
		t.Fatalf("own registry is not eligible: %+v", c)
	}
}

func TestRediscoveryCurrentMarker(t *testing.T) {
	rekey := marker(testDeployment, otherKFP, testUser)
	rekey.Writer = "sys.rekey"
	upgrade := marker(testDeployment, testKFP, testUser)
	upgrade.FormatVersion, upgrade.MinReaderVersion = 2, 1
	downgrade := marker(testDeployment, testKFP, testUser)
	downgrade.FormatVersion = 0
	tests := []struct {
		name      string
		revisions []wire.Op
		wantKFP   string
		wantFV    int
		wantMsgIx int
	}{
		{name: "no revision", wantKFP: testKFP, wantFV: 1, wantMsgIx: 0},
		{name: "rekey revision accepted", revisions: []wire.Op{rekey}, wantKFP: otherKFP, wantFV: 1, wantMsgIx: 1},
		{name: "unsigned key change ignored", revisions: []wire.Op{marker(testDeployment, otherKFP, testUser)}, wantKFP: testKFP, wantFV: 1, wantMsgIx: 0},
		{name: "other deployment ignored", revisions: []wire.Op{marker(otherDeploy, testKFP, testUser)}, wantKFP: testKFP, wantFV: 1, wantMsgIx: 0},
		{name: "other user ignored", revisions: []wire.Op{marker(testDeployment, testKFP, otherUser)}, wantKFP: testKFP, wantFV: 1, wantMsgIx: 0},
		{name: "format upgrade accepted", revisions: []wire.Op{upgrade}, wantKFP: testKFP, wantFV: 2, wantMsgIx: 1},
		{name: "format downgrade ignored", revisions: []wire.Op{upgrade, marker(testDeployment, testKFP, testUser)}, wantKFP: testKFP, wantFV: 2, wantMsgIx: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			registry := w.channel(t, "butterDB registry")
			ids := []int64{w.op(t, registry, marker(testDeployment, testKFP, testUser))}
			for _, rev := range tt.revisions {
				ids = append(ids, w.op(t, registry, rev))
			}
			got, err := newDiscoverer(t, w, Identity{KeyFingerprint: tt.wantKFP, UserID: testUser}).Discover(context.Background())
			if err != nil || len(got) != 1 {
				t.Fatalf("discover: %v %+v", err, got)
			}
			c := got[0]
			if c.KeyFingerprint != tt.wantKFP || c.FormatVersion != tt.wantFV || c.MarkerMsgID != ids[tt.wantMsgIx] {
				t.Fatalf("current marker = kfp %s fv %d msg %d, want kfp %s fv %d msg %d", c.KeyFingerprint, c.FormatVersion, c.MarkerMsgID, tt.wantKFP, tt.wantFV, ids[tt.wantMsgIx])
			}
		})
	}
}

func TestRediscoveryReadsWholeChannelForCurrentMarker(t *testing.T) {
	w := newWorld(t)
	registry := w.channel(t, "butterDB registry")
	w.op(t, registry, marker(testDeployment, testKFP, testUser))
	for i := 0; i < 250; i++ {
		w.say(t, registry, fmt.Sprintf("filler %d", i))
	}
	rekey := marker(testDeployment, otherKFP, testUser)
	rekey.Writer = "sys.rekey"
	late := w.op(t, registry, rekey)

	got, err := newDiscoverer(t, w, Identity{KeyFingerprint: otherKFP, UserID: testUser}).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("discover: %v %+v", err, got)
	}
	if got[0].MarkerMsgID != late || got[0].KeyFingerprint != otherKFP {
		t.Fatalf("late revision not found: %+v", got[0])
	}
}

func TestRediscoveryIgnoresChannelWithoutEarlyMarker(t *testing.T) {
	w := newWorld(t)
	ch := w.channel(t, "late marker")
	for i := 0; i < 150; i++ {
		w.say(t, ch, "filler")
	}
	w.op(t, ch, marker(testDeployment, testKFP, testUser))

	got, err := newDiscoverer(t, w, ownIdentity()).Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("channel with a marker past the first 100 messages became a candidate: %+v", got)
	}
}

func TestRegistryRediscoveryRejectsForeignKey(t *testing.T) {
	tests := []struct {
		name   string
		kfp    string
		uid    int64
		reason ForeignReason
	}{
		{name: "foreign key fingerprint", kfp: otherKFP, uid: testUser, reason: ForeignKey},
		{name: "foreign user", kfp: testKFP, uid: otherUser, reason: ForeignAccount},
		{name: "foreign key and user", kfp: otherKFP, uid: otherUser, reason: ForeignKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			foreign := w.channel(t, "butterDB registry")
			w.op(t, foreign, marker(testDeployment, tt.kfp, tt.uid))
			own := w.channel(t, "butterDB registry")
			w.op(t, own, marker(otherDeploy, testKFP, testUser))

			d := newDiscoverer(t, w, ownIdentity())
			all, err := d.Discover(context.Background())
			if err != nil || len(all) != 2 {
				t.Fatalf("discover: %v %+v", err, all)
			}
			if all[0].Foreign != tt.reason || all[0].Eligible() {
				t.Fatalf("foreign candidate = %+v, want foreign %q", all[0], tt.reason)
			}
			if eligible := Eligible(all); len(eligible) != 1 || eligible[0].Peer.ChannelID != own.ChannelID {
				t.Fatalf("eligible = %+v, want only the own registry", eligible)
			}
			adoption, err := d.Adopt(all, foreign.ChannelID)
			if !errors.Is(err, ErrForeignRegistry) {
				t.Fatalf("adopt foreign = %v, want ErrForeignRegistry", err)
			}
			if adoption != (Adoption{}) {
				t.Fatalf("foreign adoption returned data: %+v", adoption)
			}
		})
	}
}

func TestRegistryRediscoveryDoesNotTrustForgedForeignFlag(t *testing.T) {
	w := newWorld(t)
	foreign := w.channel(t, "butterDB registry")
	w.op(t, foreign, marker(testDeployment, otherKFP, testUser))
	d := newDiscoverer(t, w, ownIdentity())
	all, err := d.Discover(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("discover: %v %+v", err, all)
	}
	all[0].Foreign = NotForeign
	if _, err := d.Adopt(all, foreign.ChannelID); !errors.Is(err, ErrForeignRegistry) {
		t.Fatalf("adopt with cleared flag = %v, want ErrForeignRegistry", err)
	}
}

func TestRediscoveryRequiresConfirmation(t *testing.T) {
	w := newWorld(t)
	registry := w.channel(t, "butterDB registry")
	markerID := w.op(t, registry, marker(testDeployment, testKFP, testUser))
	d := newDiscoverer(t, w, ownIdentity())
	all, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	tests := []struct {
		name      string
		confirmed int64
		want      error
	}{
		{name: "no confirmation", confirmed: 0, want: ErrConfirmationRequired},
		{name: "negative confirmation", confirmed: -1, want: ErrConfirmationRequired},
		{name: "unlisted channel", confirmed: registry.ChannelID + 99, want: ErrNotACandidate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adoption, err := d.Adopt(all, tt.confirmed)
			if !errors.Is(err, tt.want) {
				t.Fatalf("adopt = %v, want %v", err, tt.want)
			}
			if adoption != (Adoption{}) {
				t.Fatalf("adoption returned data without confirmation: %+v", adoption)
			}
		})
	}

	t.Run("typed channel id adopts", func(t *testing.T) {
		got, err := d.Adopt(all, registry.ChannelID)
		if err != nil {
			t.Fatalf("adopt: %v", err)
		}
		want := Adoption{
			RegistryChannelID: registry.ChannelID, RegistryAccessHash: registry.AccessHash, RegistryMarkerMsgID: markerID,
			PrimaryUserID: testUser, ServerKeyFingerprint: testKFP, DeploymentID: testDeployment, FormatVersion: 1, MinReaderVersion: 1,
		}
		if got != want {
			t.Fatalf("adoption = %+v, want %+v", got, want)
		}
	})

	t.Run("discovery alone changes nothing on telegram", func(t *testing.T) {
		if len(registrySends(w, registry)) != 1 {
			t.Fatalf("discovery sent messages to the registry")
		}
	})
}

func registrySends(w *fakeWorld, peer tgclient.InputPeer) []tgclient.HistoryMessage {
	msgs, err := w.alice.GetHistory(context.Background(), peer, 0, 100)
	if err != nil {
		panic(err)
	}
	return msgs
}

func TestRediscoveryAllForeignHasNoEligible(t *testing.T) {
	w := newWorld(t)
	foreign := w.channel(t, "butterDB registry")
	w.op(t, foreign, marker(testDeployment, otherKFP, testUser))
	all, err := newDiscoverer(t, w, ownIdentity()).Discover(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("discover: %v %+v", err, all)
	}
	if got := Eligible(all); len(got) != 0 {
		t.Fatalf("eligible = %+v, want none", got)
	}
}

func TestRediscoveryNoCandidates(t *testing.T) {
	w := newWorld(t)
	got, err := newDiscoverer(t, w, ownIdentity()).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("discover on empty account = %v %+v", err, got)
	}
}

func TestRediscoveryTelegramErrorsAreNotSwallowed(t *testing.T) {
	boom := errors.New("transport down")
	tests := []struct {
		name   string
		method faketg.Method
	}{
		{name: "list owned channels fails", method: faketg.MethodListOwnedChannels},
		{name: "history read fails", method: faketg.MethodGetHistory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			registry := w.channel(t, "butterDB registry")
			w.op(t, registry, marker(testDeployment, testKFP, testUser))
			if err := w.alice.QueueFaults(tt.method, faketg.Fault{Kind: faketg.FaultError, Err: boom}); err != nil {
				t.Fatalf("queue fault: %v", err)
			}
			got, err := newDiscoverer(t, w, ownIdentity()).Discover(context.Background())
			if !errors.Is(err, boom) {
				t.Fatalf("discover = %v %+v, want transport error", err, got)
			}
		})
	}
}

func TestRediscoveryFailsOnHistoryWithoutProgress(t *testing.T) {
	w := newWorld(t)
	w.channel(t, "stuck")
	text, err := wire.Format(withTS(marker(testDeployment, testKFP, testUser)))
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	reader := fixedHistory{RegistryReader: w.alice, text: text}
	d, err := NewDiscoverer(DiscoveryDependencies{Telegram: reader, Identity: ownIdentity(), Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new discoverer: %v", err)
	}
	if _, err := d.Discover(context.Background()); !errors.Is(err, syncer.ErrHistoryNoProgress) {
		t.Fatalf("discover = %v, want ErrHistoryNoProgress", err)
	}
}

func withTS(op wire.Op) wire.Op {
	op.Timestamp = testTS
	return op
}

type fixedHistory struct {
	RegistryReader
	text string
}

func (f fixedHistory) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	return []tgclient.HistoryMessage{{MsgID: 7, Text: f.text}}, nil
}

func TestNewDiscovererRejectsMissingDependencies(t *testing.T) {
	w := newWorld(t)
	tests := []struct {
		name string
		deps DiscoveryDependencies
	}{
		{name: "telegram", deps: DiscoveryDependencies{Identity: ownIdentity(), Logger: discardLogger()}},
		{name: "fingerprint", deps: DiscoveryDependencies{Telegram: w.alice, Identity: Identity{UserID: testUser}, Logger: discardLogger()}},
		{name: "user", deps: DiscoveryDependencies{Telegram: w.alice, Identity: Identity{KeyFingerprint: testKFP}, Logger: discardLogger()}},
		{name: "logger", deps: DiscoveryDependencies{Telegram: w.alice, Identity: ownIdentity()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDiscoverer(tt.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewDiscoverer = %v, want ErrMissingDependency", err)
			}
		})
	}
}

type aboutLister struct {
	inner tgclient.ChannelRPC
	about map[int64]string
}

func (a aboutLister) ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error) {
	channels, err := a.inner.ListOwnedChannels(ctx)
	for i := range channels {
		channels[i].About = a.about[channels[i].Peer.ChannelID]
	}
	return channels, err
}

type referenced []int64

func (r referenced) ReferencedBucketChannels(context.Context) ([]int64, error) {
	return r, nil
}

func TestOrphanBucketChannelsAreListedOnly(t *testing.T) {
	w := newWorld(t)
	about := func(name, dep string) string {
		return fmt.Sprintf("butterDB bucket %s in deployment %s. Do not post, edit or delete messages here.", name, dep)
	}
	kept := w.channel(t, "butterDB bucket kept")
	orphan := w.channel(t, "butterDB bucket orphan")
	foreignDeploy := w.channel(t, "butterDB bucket other")
	unverified := w.channel(t, "butterDB bucket unknown")
	registry := w.channel(t, "butterDB registry")
	plain := w.channel(t, "holiday photos")
	lister := aboutLister{inner: w.alice, about: map[int64]string{
		kept.ChannelID:          about("kept", testDeployment),
		orphan.ChannelID:        about("orphan", testDeployment),
		foreignDeploy.ChannelID: about("other", otherDeploy),
		registry.ChannelID:      "butterDB registry " + testDeployment,
		plain.ChannelID:         "butterDB bucket sneaky in deployment " + testDeployment + ".",
	}}
	finder, err := NewOrphanFinder(OrphanDependencies{Telegram: lister, Buckets: referenced{kept.ChannelID}, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new orphan finder: %v", err)
	}
	got, err := finder.List(context.Background(), testDeployment)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []OrphanChannel{
		{Peer: orphan, Title: "butterDB bucket orphan", BucketName: "orphan", AboutVerified: true},
		{Peer: unverified, Title: "butterDB bucket unknown", BucketName: "unknown", AboutVerified: false},
	}
	if len(got) != len(want) {
		t.Fatalf("orphans = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orphan %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, id := range w.net.ChannelIDs() {
		if _, err := w.alice.GetHistory(context.Background(), tgclient.InputPeer{ChannelID: id, AccessHash: peerHash(w, id)}, 0, 1); err != nil {
			t.Fatalf("channel %d no longer readable after listing: %v", id, err)
		}
	}
}

func peerHash(w *fakeWorld, id int64) int64 {
	peer, err := w.alice.PeerOf(id)
	if err != nil {
		panic(err)
	}
	return peer.AccessHash
}

func TestNewOrphanFinderRejectsMissingDependencies(t *testing.T) {
	w := newWorld(t)
	tests := []struct {
		name string
		deps OrphanDependencies
	}{
		{name: "telegram", deps: OrphanDependencies{Buckets: referenced{}, Logger: discardLogger()}},
		{name: "buckets", deps: OrphanDependencies{Telegram: w.alice, Logger: discardLogger()}},
		{name: "logger", deps: OrphanDependencies{Telegram: w.alice, Buckets: referenced{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewOrphanFinder(tt.deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewOrphanFinder = %v, want ErrMissingDependency", err)
			}
		})
	}
	finder, err := NewOrphanFinder(OrphanDependencies{Telegram: w.alice, Buckets: referenced{}, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new orphan finder: %v", err)
	}
	if _, err := finder.List(context.Background(), ""); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("List without deployment = %v", err)
	}
}
