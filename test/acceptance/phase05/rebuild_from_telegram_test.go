package phase05

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type linkedAccount struct{}

func (linkedAccount) HasPrimaryAccount(context.Context) (bool, error) { return true, nil }

var snapshotQueries = map[string]string{
	"buckets":     `SELECT name, channel_id, policy, encryption_default, cache_max_age_seconds, deleted_msg_id, object_count, stored_bytes FROM buckets ORDER BY channel_id`,
	"objects":     `SELECT channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, etag, writer FROM objects ORDER BY channel_id, object_key`,
	"keys":        `SELECT bucket_channel_id, key_id, hex(wrapped_dek), hex(key_check), kek_fingerprint, config_version, msg_id FROM encryption_keys ORDER BY bucket_channel_id, key_id`,
	"markers":     `SELECT channel_id, msg_id, deployment_id, kek_fingerprint, primary_user_id FROM registry_markers ORDER BY msg_id`,
	"tombstones":  `SELECT channel_id, object_key, del_msg_id FROM object_tombstones ORDER BY channel_id, object_key`,
	"replay size": `SELECT channel_id, COUNT(*) FROM replay_log GROUP BY channel_id ORDER BY channel_id`,
}

func (w *world) snapshot() map[string][]string {
	w.t.Helper()
	out := make(map[string][]string, len(snapshotQueries))
	for name, query := range snapshotQueries {
		out[name] = w.rows(query)
	}
	return out
}

func TestDeletedLocalDatabaseRebuildsFromTelegram(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	photos, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
	if err != nil {
		t.Fatalf("Create photos: %v", err)
	}
	vault, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "vault", Policy: "private", DefaultEncryption: "tde1", CacheMaxAge: 60})
	if err != nil {
		t.Fatalf("Create vault: %v", err)
	}
	if _, err := w.sync.Buckets.Update(ctx, "photos", bucket.Patch{Policy: ptr("public-read")}); err != nil {
		t.Fatalf("Update photos: %v", err)
	}
	bodyMsgID := w.sendExternal(photos.ChannelID, format(t, wire.Op{Type: wire.OpBody, JobID: externalPutJob, StoredSize: 100}))
	w.sendExternal(photos.ChannelID, format(t, putOp("reports/q3.txt", bodyMsgID, externalPutJob)))
	w.sendExternal(photos.ChannelID, format(t, delOp("gone.txt", "1f8fad5b-d9cb-469f-a165-70867728950e")))
	if err := w.sync.Difference.Sync(ctx, photos.ChannelID); err != nil {
		t.Fatalf("Sync photos: %v", err)
	}
	before := w.snapshot()
	if len(before["objects"]) != 1 || len(before["keys"]) != 1 || len(before["markers"]) != 1 || len(before["buckets"]) != 2 {
		t.Fatalf("the original projection is not what the test expects: %v", before)
	}

	if err := w.db.Close(); err != nil {
		t.Fatalf("close projection: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(w.dir + "/projection.db" + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove projection.db%s: %v", suffix, err)
		}
	}
	w.openProjection()
	w.buildSync()
	if got := w.count(`SELECT COUNT(*) FROM channels`); got != 0 {
		t.Fatalf("channels in the fresh database = %d, want 0", got)
	}

	state, found, err := w.stateStore().LoadRegistryState(ctx)
	if err != nil || !found {
		t.Fatalf("state.json after deleting the database = %v, %v", found, err)
	}
	if err := w.sync.RecordRegistry(ctx, state); err != nil {
		t.Fatalf("RecordRegistry: %v", err)
	}
	if err := w.sync.Scanner.EnsureAllAuthoritative(ctx); err != nil {
		t.Fatalf("EnsureAllAuthoritative: %v", err)
	}

	if after := w.snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("projection rebuilt from Telegram differs\nbefore: %v\nafter:  %v", before, after)
	}
	for _, channelID := range []int64{w.registryID(), photos.ChannelID, vault.ChannelID} {
		got := w.channelState(channelID)
		if !got.InitialSyncDone || got.NeedsProjectionRebuild || got.AccessHash == 0 || got.Pts == 0 {
			t.Fatalf("channel %d after the scan = %+v, want authoritative with a hash and a pts", channelID, got)
		}
	}
	checker, err := core.NewLocalStateChecker(core.LocalStateDependencies{
		State: w.stateStore(), Fingerprint: w.ring.FingerprintString(), Database: w.db, Accounts: linkedAccount{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Check(ctx); err != nil {
		t.Fatalf("startup checks after the rebuild: %v", err)
	}
	if _, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "later", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600}); err != nil {
		t.Fatalf("Create after the rebuild: %v", err)
	}
}
