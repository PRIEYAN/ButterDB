package phase03

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func (h *harness) projectMixedSequence() (kid string) {
	h.t.Helper()
	place := func(channelID, msgID int64, op wire.Op) projection.Outcome {
		outcome, _ := h.projectWithRepair(channelID, msgID, op)
		return outcome
	}
	h.project(registryChannel, 1, markerOp(1, h.ring.FingerprintString()))
	h.project(registryChannel, 2, bucketCreateOp(2, "bucket-a", channelA))
	h.project(registryChannel, 3, bucketCreateOp(3, "bucket-b", channelB))
	h.project(registryChannel, 4, bucketConfigOp(4, "bucket-a", channelA, "public-read"))
	encfg, kid, _ := h.encfgOp(5, channelA)
	h.project(registryChannel, 5, encfg)
	h.projectRaw(registryChannel, 6, "TDX1|t=future|j="+jobID(6), false)
	h.projectRaw(registryChannel, 7, "TDX1|this is not a valid header", false)
	h.project(registryChannel, 8, bucketCreateOp(8, "bucket-a", 4000))

	h.project(channelA, 10, bodyOp(10, 100))
	place(channelA, 11, putOp(11, "docs/a.txt", 10, 100))
	h.project(channelA, 12, bodyOp(12, 200))
	place(channelA, 13, putOp(13, "docs/a.txt", 12, 200))
	h.project(channelA, 14, bodyOp(14, 300))
	place(channelA, 15, putOp(15, "docs/b.txt", 14, 300))
	place(channelA, 16, putOp(15, "docs/b.txt", 14, 300))
	place(channelA, 17, delOp(17, "docs/a.txt"))
	h.project(channelA, 18, bodyOp(18, 50))
	place(channelA, 19, putOp(19, "docs/a.txt", 18, 50))
	place(channelA, 20, delOp(20, "never-existed.txt"))
	place(channelA, 21, putOp(21, "empty.txt", 0, 0))

	plaintext := int64(70000)
	encrypted := putOp(22, "secret.bin", 23, format.CiphertextSize(plaintext))
	encrypted.Encrypted, encrypted.EncryptionVersion, encrypted.PlaintextSize, encrypted.KeyID = true, 1, plaintext, kid
	h.project(channelA, 23, bodyOp(23, encrypted.StoredSize))
	encrypted.BodyMsgID = 23
	place(channelA, 24, encrypted)

	h.project(channelA, 30, bodyOp(30, 10))
	place(channelA, 35, delOp(35, "late.txt"))
	h.project(channelA, 31, bodyOp(31, 10))
	place(channelA, 32, putOp(32, "late.txt", 31, 10))

	h.project(channelB, 5, bodyOp(25, 7))
	place(channelB, 6, putOp(26, "other.txt", 5, 7))
	return kid
}

func TestRebuildMatchesTheLiveProjection(t *testing.T) {
	h := newHarness(t)
	kid := h.projectMixedSequence()
	live := h.snapshot()
	for _, table := range []string{"objects", "object_tombstones", "object_bodies", "encryption_keys", "registry_markers", "buckets", "replay_log_rejects", "projection_operations", "pending_message_cleanup"} {
		if len(live[table]) == 0 {
			t.Fatalf("scenario left table %s empty", table)
		}
	}
	if h.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE reason = 'duplicate'`) != 1 {
		t.Fatal("scenario did not keep exactly one duplicate cleanup row")
	}
	if h.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'unknown_op'`) != 1 ||
		h.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'malformed'`) != 1 ||
		h.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'bucket_exists'`) != 1 ||
		h.count(`SELECT COUNT(*) FROM replay_log_rejects WHERE code = 'duplicate_op'`) != 1 {
		t.Fatalf("scenario did not record the expected rejects: %v", live["replay_log_rejects"])
	}
	if h.count(`SELECT COUNT(*) FROM object_tombstones WHERE object_key = 'late.txt' AND del_msg_id = 35`) != 1 ||
		h.count(`SELECT COUNT(*) FROM objects WHERE object_key = 'late.txt'`) != 0 {
		t.Fatal("a put below a tombstone resurrected its key")
	}

	for round := 1; round <= 2; round++ {
		result, err := h.proj.Rebuilder.RebuildAll(context.Background(), h.db)
		if err != nil {
			t.Fatalf("rebuild all: %v", err)
		}
		if result.Applied == 0 || result.Rejected == 0 {
			t.Fatalf("rebuild result = %+v, want applied and rejected ops", result)
		}
		assertSnapshotsEqual(t, live, h.snapshot())
	}

	keys, err := bucketrepo.New(h.db)
	if err != nil {
		t.Fatalf("new bucket repository: %v", err)
	}
	stored, found, err := keys.Key(context.Background(), channelA, kid)
	if err != nil || !found {
		t.Fatalf("rebuilt key lookup = %v, %v", found, err)
	}
	dek, err := h.ring.UnwrapDEK(stored.WrappedDEK, stored.KeyCheck, channelA, kid)
	if err != nil {
		t.Fatalf("rebuilt wrapped key does not unwrap: %v", err)
	}
	dek.Clear()
}

func (h *harness) cleanupMessages() []string {
	h.t.Helper()
	rows, err := h.db.Reader().Query(`SELECT channel_id, msg_id FROM pending_message_cleanup ORDER BY channel_id, msg_id`)
	if err != nil {
		h.t.Fatalf("list cleanup queue: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var channelID, msgID int64
		if err := rows.Scan(&channelID, &msgID); err != nil {
			h.t.Fatalf("scan cleanup queue: %v", err)
		}
		ids = append(ids, fmt.Sprintf("%d/%d", channelID, msgID))
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("cleanup queue rows: %v", err)
	}
	return ids
}

func TestPerKeyRepairEqualsInOrderProjection(t *testing.T) {
	inOrder := newHarness(t)
	outOfOrder := newHarness(t)
	for _, h := range []*harness{inOrder, outOfOrder} {
		h.project(registryChannel, 1, markerOp(1, h.ring.FingerprintString()))
		h.project(registryChannel, 2, bucketCreateOp(2, "bucket-a", channelA))
		h.project(channelA, 10, bodyOp(10, 11))
		h.project(channelA, 12, bodyOp(12, 22))
		h.project(channelA, 14, bodyOp(14, 33))
		h.project(channelA, 16, bodyOp(16, 44))
		h.project(channelA, 18, bodyOp(18, 55))
	}
	sequence := []struct {
		msgID int64
		op    wire.Op
	}{
		{11, putOp(11, "k.txt", 10, 11)},
		{13, putOp(13, "k.txt", 12, 22)},
		{15, delOp(15, "k.txt")},
		{17, putOp(17, "k.txt", 16, 44)},
		{19, putOp(19, "other.txt", 18, 55)},
	}
	for _, step := range sequence {
		inOrder.project(channelA, step.msgID, step.op)
	}
	arrival := []int{3, 0, 4, 2, 1}
	repairs := 0
	for _, index := range arrival {
		step := sequence[index]
		_, repaired := outOfOrder.projectWithRepair(channelA, step.msgID, step.op)
		if repaired {
			repairs++
		}
	}
	if repairs == 0 {
		t.Fatal("no out of order arrival triggered a repair")
	}
	want, got := inOrder.snapshot(), outOfOrder.snapshot()
	if wantIDs, gotIDs := inOrder.cleanupMessages(), outOfOrder.cleanupMessages(); strings.Join(wantIDs, ",") != strings.Join(gotIDs, ",") {
		t.Errorf("cleanup queue differs: want %v, got %v", wantIDs, gotIDs)
	}
	delete(want, "pending_message_cleanup")
	delete(got, "pending_message_cleanup")
	assertSnapshotsEqual(t, want, got)
	for _, table := range []string{"objects", "object_tombstones", "pending_message_cleanup"} {
		if len(outOfOrder.snapshot()[table]) == 0 {
			t.Fatalf("scenario left table %s empty", table)
		}
	}
}
