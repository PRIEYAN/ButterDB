package rebuild

import (
	"fmt"
	"testing"
)

func TestRebuildEquivalence(t *testing.T) {
	e := newEnv(t)
	e.addChannel(otherChannel, "bucket", "active")
	e.project(registryChannel, 1, headerOf(t, bucketCreateOp(1, "bucket-a", bucketChannel)), false)
	e.project(registryChannel, 2, headerOf(t, bucketCreateOp(2, "bucket-c", otherChannel)), false)
	e.project(registryChannel, 3, headerOf(t, encfgOp(3, bucketChannel)), false)
	e.project(registryChannel, 4, headerOf(t, encfgOp(4, bucketChannel)), false)
	e.project(registryChannel, 5, "TDX1|t=future|j="+jobID(5), false)
	e.project(registryChannel, 6, headerOf(t, encfgOp(6, 777)), false)

	msgID := int64(10)
	for n := 0; n < 300; n++ {
		key := fmt.Sprintf("key-%d", n%40)
		switch {
		case n%11 == 0:
			e.project(bucketChannel, msgID, headerOf(t, bodyOp(1000+n, 50)), true)
		case n%7 == 0:
			e.project(bucketChannel, msgID, headerOf(t, delOp(1000+n, key)), false)
		case n%13 == 0:
			e.project(bucketChannel, msgID, headerOf(t, putOp(1000+n, key, msgID+5, 10)), false)
		default:
			e.project(bucketChannel, msgID, headerOf(t, putOp(1000+n, key, msgID-1, int64(n+1))), false)
		}
		msgID++
	}
	duplicate := putOp(1000+1, "key-1", msgID-1, 3)
	e.project(bucketChannel, msgID, headerOf(t, duplicate), false)
	e.project(otherChannel, 3, headerOf(t, putOp(5000, "other", 0, 0)), false)
	e.exec(`INSERT INTO replay_log_rejects (channel_id, msg_id, code, raw_header, msg_date) VALUES (?, 99999, 'malformed', 'TDX1|junk', 1)`, bucketChannel)
	e.exec(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?, 8, 'cleanup', 1)`, bucketChannel)

	before := e.snapshot()
	if e.count(`SELECT COUNT(*) FROM projection_operations WHERE outcome = 'applied'`) == 0 {
		t.Fatal("scenario recorded no applied operation rows")
	}
	if e.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE reason = 'duplicate'`) == 0 {
		t.Fatal("scenario kept no duplicate cleanup row")
	}
	if len(before["objects"]) == 0 || len(before["pending_message_cleanup"]) == 0 || len(before["object_tombstones"]) == 0 || len(before["replay_log_rejects"]) < 3 {
		t.Fatalf("scenario too thin: %d objects, %d cleanup, %d tombstones, %d rejects",
			len(before["objects"]), len(before["pending_message_cleanup"]), len(before["object_tombstones"]), len(before["replay_log_rejects"]))
	}

	e.rebuildAll()
	assertSnapshotsEqual(t, before, e.snapshot())

	e.rebuildAll()
	assertSnapshotsEqual(t, before, e.snapshot())
}
