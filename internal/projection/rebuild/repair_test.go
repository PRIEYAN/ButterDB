package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func (e *env) needsRepair(channelID int64, key string, msgID int64) bool {
	e.t.Helper()
	var needed bool
	e.write(func(tx *sql.Tx) error {
		var err error
		needed, err = e.rebuild.NeedsKeyRepair(context.Background(), tx, channelID, key, msgID)
		return err
	})
	return needed
}

func (e *env) ingest(channelID, msgID int64, rawHeader, key string) {
	e.t.Helper()
	needed := e.needsRepair(channelID, key, msgID)
	e.appendReplay(channelID, msgID, rawHeader, false)
	if !needed {
		e.applyLogged(channelID, msgID)
		return
	}
	if _, err := e.rebuild.RepairKey(context.Background(), e.db, channelID, key); err != nil {
		e.t.Fatalf("repair %s: %v", key, err)
	}
}

func (e *env) cleanupIDs() []int64 {
	e.t.Helper()
	rows, err := e.db.Reader().Query(`SELECT msg_id FROM pending_message_cleanup ORDER BY channel_id, msg_id`)
	if err != nil {
		e.t.Fatalf("cleanup ids: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestPerKeyRepairOnlyTouchesKey(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 10, headerOf(t, bodyOp(10, 5)), true)
	e.project(bucketChannel, 11, headerOf(t, putOp(11, "a", 10, 5)), false)
	e.project(bucketChannel, 12, headerOf(t, putOp(12, "b", 10, 7)), false)
	e.project(bucketChannel, 13, headerOf(t, putOp(13, "c", 0, 0)), false)
	e.project(bucketChannel, 14, headerOf(t, delOp(14, "c")), false)
	e.exec(`UPDATE objects SET content_type = 'sentinel' WHERE object_key = 'b'`)
	e.exec(`UPDATE object_tombstones SET writer = 'sentinel' WHERE object_key = 'c'`)
	e.exec(`UPDATE object_bodies SET stored_size = 99 WHERE msg_id = 10`)
	untouched := func() map[string][]string {
		return map[string][]string{
			"b objects":    e.dump("objects")[1:],
			"tombstones":   e.dump("object_tombstones"),
			"bodies":       e.dump("object_bodies"),
			"registry ops": e.dump("registry_markers"),
		}
	}
	before := untouched()
	countsBefore := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?`, bucketChannel)

	if !e.needsRepair(bucketChannel, "a", 9) {
		t.Fatal("a put below an already recorded op must need repair")
	}
	e.appendReplay(bucketChannel, 9, headerOf(t, putOp(9, "a", 0, 0)), false)
	e.applier.reset()
	result, err := e.rebuild.RepairKey(context.Background(), e.db, bucketChannel, "a")
	if err != nil {
		t.Fatalf("repair: %v", err)
	}

	if result.Applied != 2 {
		t.Fatalf("repair result = %+v, want 2 applied", result)
	}
	for _, call := range e.applier.calls {
		if call.msgID != 9 && call.msgID != 11 {
			t.Fatalf("repair applied msg %d, which does not belong to key a", call.msgID)
		}
	}
	if got := e.objectPut(bucketChannel, "a"); got != 11 {
		t.Fatalf("object a put_msg_id = %d, want 11", got)
	}
	assertSnapshotsEqual(t, before, untouched())
	if got := e.count(`SELECT object_count FROM buckets WHERE channel_id = ?`, bucketChannel); got != countsBefore {
		t.Fatalf("object_count = %d, want %d", got, countsBefore)
	}
	if n := e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ? AND msg_id = 9`, bucketChannel); n != 1 {
		t.Fatal("superseded older put was not queued for cleanup")
	}
}

func TestRepairNotTriggeredForOwnWritesAboveWatermark(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.addChannel(otherChannel, "bucket", "active")
	e.exec(`UPDATE channels SET last_synced_msg = 20 WHERE channel_id = ?`, bucketChannel)
	e.project(bucketChannel, 30, headerOf(t, putOp(30, "k", 0, 0)), false)
	e.project(bucketChannel, 31, headerOf(t, putOp(31, "other-key", 0, 0)), false)
	e.project(otherChannel, 99, headerOf(t, putOp(99, "k", 0, 0)), false)

	cases := []struct {
		name      string
		channelID int64
		key       string
		msgID     int64
		want      bool
	}{
		{"own write above every recorded op", bucketChannel, "k", 32, false},
		{"own write above the watermark but in order", bucketChannel, "k", 31, false},
		{"same message id is not lower", bucketChannel, "k", 30, false},
		{"higher op belongs to another key", bucketChannel, "fresh", 5, false},
		{"higher op belongs to another channel", bucketChannel, "k", 98, false},
		{"lower than a recorded op above the watermark", bucketChannel, "k", 29, true},
		{"lower than a recorded op below nothing else", otherChannel, "k", 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.needsRepair(tc.channelID, tc.key, tc.msgID); got != tc.want {
				t.Fatalf("NeedsKeyRepair() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRepairKeyEqualsInOrderApply(t *testing.T) {
	type step struct {
		msgID  int64
		header func(t *testing.T) string
	}
	steps := []step{
		{20, func(t *testing.T) string { return headerOf(t, putOp(20, "k", 0, 0)) }},
		{21, func(t *testing.T) string { return headerOf(t, putOp(21, "k", 15, 4)) }},
		{22, func(t *testing.T) string { return headerOf(t, delOp(22, "k")) }},
		{23, func(t *testing.T) string { return headerOf(t, putOp(23, "k", 16, 9)) }},
		{24, func(t *testing.T) string { return headerOf(t, putOp(24, "k", 17, 2)) }},
		{25, func(t *testing.T) string { return headerOf(t, delOp(25, "other")) }},
	}
	build := func(order []int) *env {
		e := newEnv(t)
		e.seedBucket("bucket-a", bucketChannel, 1)
		for _, i := range order {
			s := steps[i]
			key := "k"
			if s.msgID == 25 {
				key = "other"
			}
			e.ingest(bucketChannel, s.msgID, s.header(t), key)
		}
		return e
	}
	inOrder := build([]int{0, 1, 2, 3, 4, 5})
	if inOrder.count(`SELECT COUNT(*) FROM projection_operations WHERE outcome = 'applied'`) == 0 {
		t.Fatal("scenario recorded no applied operation rows")
	}
	wantCleanup := inOrder.cleanupIDs()
	want := inOrder.snapshot("objects", "object_tombstones", "projection_operations", "buckets")

	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 12; round++ {
		order := rng.Perm(len(steps))
		t.Run(fmt.Sprintf("order %v", order), func(t *testing.T) {
			shuffled := build(order)
			if got := shuffled.cleanupIDs(); !reflect.DeepEqual(got, wantCleanup) {
				t.Fatalf("cleanup set = %v, want %v", got, wantCleanup)
			}
			assertSnapshotsEqual(t, want, shuffled.snapshot("objects", "object_tombstones", "projection_operations", "buckets"))
		})
	}
}

func TestRepairKeyRejectsRegistryAndDeletedChannels(t *testing.T) {
	e := newEnv(t)
	e.addChannel(otherChannel, "bucket", "deleted")
	cases := []struct {
		name      string
		channelID int64
	}{
		{"registry channel", registryChannel},
		{"deleted channel", otherChannel},
		{"unknown channel", 424242},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.rebuild.RepairKey(context.Background(), e.db, tc.channelID, "k")
			if !errors.Is(err, errs.ErrNoSuchBucket) {
				t.Fatalf("RepairKey() error = %v, want ErrNoSuchBucket", err)
			}
		})
	}
	if _, err := e.rebuild.RepairKey(context.Background(), nil, bucketChannel, "k"); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil writer error = %v", err)
	}
}

func TestRepairKeyRollsBackOnApplierFailure(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(bucketChannel, 10, headerOf(t, putOp(10, "k", 0, 0)), false)
	e.project(bucketChannel, 12, headerOf(t, putOp(12, "k", 0, 0)), false)
	before := e.snapshot()
	e.appendReplay(bucketChannel, 11, headerOf(t, putOp(11, "k", 0, 0)), false)
	before["replay_log"] = e.dump("replay_log")
	e.applier.failMsg = 12

	if _, err := e.rebuild.RepairKey(context.Background(), e.db, bucketChannel, "k"); !errors.Is(err, errInjected) {
		t.Fatalf("RepairKey() error = %v, want injected failure", err)
	}
	e.applier.failMsg = 0
	assertSnapshotsEqual(t, before, e.snapshot())
	if !reflect.DeepEqual(before["objects"], e.dump("objects")) {
		t.Fatal("failed repair changed objects")
	}
}
