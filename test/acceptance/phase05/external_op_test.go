package phase05

import (
	"context"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const externalPutJob = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestExternalOperationIsIngested(t *testing.T) {
	ctx := context.Background()

	t.Run("a put sent by another writer is projected by one difference pass", func(t *testing.T) {
		w := newWorld(t)
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		bodyMsgID := w.sendExternal(info.ChannelID, format(t, wire.Op{Type: wire.OpBody, JobID: externalPutJob, StoredSize: 100}))
		putMsgID := w.sendExternal(info.ChannelID, format(t, putOp("reports/q3.txt", bodyMsgID, externalPutJob)))
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("objects before the pass = %d, want 0", got)
		}
		if err := w.sync.Difference.Sync(ctx, info.ChannelID); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		got := w.rows(`SELECT object_key, put_msg_id, body_msg_id, writer FROM objects WHERE channel_id = ?1`, info.ChannelID)
		if len(got) != 1 || got[0] != "reports/q3.txt|"+itoa(putMsgID)+"|"+itoa(bodyMsgID)+"|key.external" {
			t.Fatalf("objects = %v", got)
		}
		state := w.channelState(info.ChannelID)
		if state.LastSyncedMsg < putMsgID || state.Pts == 0 {
			t.Fatalf("cursor after the pass = %+v, want the watermark at or past %d", state, putMsgID)
		}
		if err := w.sync.Difference.Sync(ctx, info.ChannelID); err != nil {
			t.Fatalf("second Sync: %v", err)
		}
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 1 {
			t.Fatalf("objects after a repeat pass = %d, want 1", got)
		}
	})

	t.Run("a configuration change and an unknown op on the registry are ingested", func(t *testing.T) {
		w := newWorld(t)
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		registry := w.registryID()
		w.sendExternal(registry, format(t, wire.Op{
			Type: wire.OpBucketConfig, JobID: externalPutJob, Bucket: "photos", BucketChannelID: info.ChannelID,
			Policy: "public-read", DefaultEncryption: "none", CacheMaxAge: 120, Writer: "dev.external", Timestamp: baseTime,
		}))
		w.sendExternal(registry, "TDX1|t=future|j="+externalPutJob+"|x=1")
		if err := w.sync.Difference.Sync(ctx, registry); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		got := w.rows(`SELECT policy, cache_max_age_seconds FROM buckets WHERE channel_id = ?1`, info.ChannelID)
		if len(got) != 1 || got[0] != "public-read|120" {
			t.Fatalf("bucket after the external bcfg = %v", got)
		}
		rejects := w.rows(`SELECT code FROM replay_log_rejects WHERE channel_id = ?1`, registry)
		if len(rejects) != 1 || rejects[0] != "unknown_op" {
			t.Fatalf("rejects = %v, want the unknown op kept as a skippable reject", rejects)
		}
	})

	t.Run("a live signal makes the coordinator ingest without a manual pass", func(t *testing.T) {
		w := newWorld(t)
		info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := w.sync.Coordinator.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = w.sync.Coordinator.Close(context.Background()) })
		bodyMsgID := w.sendExternal(info.ChannelID, format(t, wire.Op{Type: wire.OpBody, JobID: externalPutJob, StoredSize: 100}))
		w.sendExternal(info.ChannelID, format(t, putOp("signal.txt", bodyMsgID, externalPutJob)))
		w.activity.Signal(info.ChannelID)
		deadline := time.Now().Add(10 * time.Second)
		for w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the coordinator never ingested the external put")
			}
			w.clk.Advance(time.Second)
			time.Sleep(20 * time.Millisecond)
		}
	})
}
