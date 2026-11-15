package phase05

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

const bucketTables = `SELECT name, channel_id, policy, encryption_default, cache_max_age_seconds, deleted_msg_id FROM buckets ORDER BY channel_id`

func TestBucketCreationIsReplayable(t *testing.T) {
	ctx := context.Background()

	t.Run("creation publishes bcreate and a rebuild reproduces the bucket", func(t *testing.T) {
		w := newWorld(t)
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if info.ChannelID == 0 || info.Name != "photos" {
			t.Fatalf("info = %+v", info)
		}
		if got := w.count(`SELECT COUNT(*) FROM channels WHERE channel_id = ?1 AND kind = 'bucket' AND initial_sync_done = 1`, info.ChannelID); got != 1 {
			t.Fatalf("authoritative bucket channel rows = %d, want 1", got)
		}
		if got := w.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1 AND op_type = 'bcreate'`, w.registryID()); got != 1 {
			t.Fatalf("bcreate ops in the replay log = %d, want 1", got)
		}
		if _, err := w.sync.Buckets.Update(ctx, "photos", bucket.Patch{Policy: ptr("public-read")}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		before := w.rows(bucketTables)
		logged := w.count(`SELECT COUNT(*) FROM replay_log`)
		if _, err := w.projection.Rebuilder.RebuildAll(ctx, w.db); err != nil {
			t.Fatalf("RebuildAll: %v", err)
		}
		if after := w.rows(bucketTables); !reflect.DeepEqual(before, after) {
			t.Fatalf("buckets after rebuild = %v, want %v", after, before)
		}
		if got := w.count(`SELECT COUNT(*) FROM replay_log`); got != logged {
			t.Fatalf("replay log rows = %d after rebuild, want %d", got, logged)
		}
		if got := w.count(`SELECT COUNT(*) FROM buckets WHERE name = 'photos' AND policy = 'public-read'`); got != 1 {
			t.Fatalf("configured bucket rows after rebuild = %d, want 1", got)
		}
	})

	t.Run("an encrypted bucket replays its key", func(t *testing.T) {
		w := newWorld(t)
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "vault", Policy: "private", DefaultEncryption: "tde1", CacheMaxAge: 60})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		keys := `SELECT key_id, kek_fingerprint, config_version FROM encryption_keys WHERE bucket_channel_id = ?1`
		before := w.rows(keys, info.ChannelID)
		if len(before) != 1 {
			t.Fatalf("encryption keys = %v, want exactly one", before)
		}
		if _, err := w.projection.Rebuilder.RebuildAll(ctx, w.db); err != nil {
			t.Fatalf("RebuildAll: %v", err)
		}
		if after := w.rows(keys, info.ChannelID); !reflect.DeepEqual(before, after) {
			t.Fatalf("keys after rebuild = %v, want %v", after, before)
		}
	})

	t.Run("a crash after the channel exists resumes without a second channel or bcreate", func(t *testing.T) {
		w := newWorld(t)
		channelsBefore := len(w.network.ChannelIDs())
		controlsBefore := len(w.network.SentControls(w.registryID()))
		unknown := faketg.Fault{Kind: faketg.FaultOutcomeUnknown}
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, unknown, unknown, unknown); err != nil {
			t.Fatalf("queue faults: %v", err)
		}
		_, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "docs", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("first Create = %v, want ErrSendOutcomeUnknown", err)
		}
		if got := w.count(`SELECT COUNT(*) FROM buckets`); got != 0 {
			t.Fatalf("buckets committed after an unknown outcome = %d, want 0", got)
		}
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "docs", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if err != nil {
			t.Fatalf("resumed Create: %v", err)
		}
		if got := len(w.network.ChannelIDs()) - channelsBefore; got != 1 {
			t.Fatalf("channels created across both attempts = %d, want 1", got)
		}
		if got := len(w.network.SentControls(w.registryID())) - controlsBefore; got != 1 {
			t.Fatalf("controls delivered on the registry = %d, want 1", got)
		}
		if got := w.count(`SELECT COUNT(*) FROM buckets WHERE channel_id = ?1 AND deleted_msg_id = 0`, info.ChannelID); got != 1 {
			t.Fatalf("committed bucket rows = %d, want 1", got)
		}
		if _, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "docs", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600}); !errors.Is(err, errs.ErrBucketExists) {
			t.Fatalf("third Create = %v, want a bucket exists error", err)
		}
	})
}

func ptr[T any](value T) *T {
	return &value
}
