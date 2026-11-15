package phase06

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestDeleteTombstonePreventsResurrection(t *testing.T) {
	w := newWorld(t)
	info := w.createBucket("photos", "none")
	meta := w.mustPut("photos", "doc.txt", []byte("to be removed"))
	if err := w.client.QueueFaults(faketg.MethodDeleteMessages, faketg.Fault{Kind: faketg.FaultError, Err: errDeleteRefused}); err != nil {
		t.Fatalf("queue fault: %v", err)
	}

	if err := w.write.Remove.Delete(w.callerContext(), "photos", "doc.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var tombstoneMsgID int64
	for _, control := range w.network.SentControls(info.ChannelID) {
		if strings.HasPrefix(control.Text, "TDX1|t=del|") {
			tombstoneMsgID = control.MsgID
		}
	}
	if tombstoneMsgID <= meta.PutMsgID {
		t.Fatalf("tombstone message %d does not follow the put %d", tombstoneMsgID, meta.PutMsgID)
	}
	if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
		t.Fatalf("objects after delete = %d, want 0", got)
	}
	if got := w.projected(`SELECT del_msg_id, target_put_msg_id FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'doc.txt'`, info.ChannelID); len(got) != 1 || got[0] != itoa(tombstoneMsgID)+"|"+itoa(meta.PutMsgID) {
		t.Fatalf("tombstone row = %v", got)
	}
	ids := w.messageIDs(info.ChannelID)
	if !ids[meta.PutMsgID] || !ids[meta.BodyMsgID] || !ids[tombstoneMsgID] {
		t.Fatalf("the failed batch must leave the messages in place behind the tombstone, have %v", ids)
	}
	if batches := w.network.DeletedBatches(info.ChannelID); len(batches) != 0 {
		t.Fatalf("delete batches reached Telegram before the tombstone settled: %v", batches)
	}
	if got := w.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1 AND reason = 'deleted'`, info.ChannelID); got != 2 {
		t.Fatalf("queued deleted-reason cleanup rows = %d, want the put and body", got)
	}

	if _, err := w.projection.Rebuilder.RebuildAll(context.Background(), w.db); err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
		t.Fatalf("objects after a rebuild with the old put still in the log = %d, want 0", got)
	}

	w.clk.Advance(2 * time.Hour)
	w.write.Sweeper.RunOnce(context.Background())
	ids = w.messageIDs(info.ChannelID)
	if ids[meta.PutMsgID] || ids[meta.BodyMsgID] {
		t.Fatalf("object messages survive the sweeper drain: %v", ids)
	}
	if !ids[tombstoneMsgID] {
		t.Fatal("the tombstone message was deleted; it must stay (risk R11)")
	}
	if _, err := w.projection.Rebuilder.RebuildAll(context.Background(), w.db); err != nil {
		t.Fatalf("RebuildAll after cleanup: %v", err)
	}
	if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
		t.Fatalf("objects after cleanup and rebuild = %d, want 0", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ?1 AND del_msg_id = ?2`, info.ChannelID, tombstoneMsgID); got != 1 {
		t.Fatalf("tombstone rows after rebuild = %d, want 1", got)
	}

	controlsBefore := len(w.network.SentControls(info.ChannelID))
	if err := w.write.Remove.Delete(w.callerContext(), "photos", "doc.txt"); err != nil {
		t.Fatalf("Delete of a missing key: %v", err)
	}
	if got := len(w.network.SentControls(info.ChannelID)); got != controlsBefore {
		t.Fatalf("deleting a missing key sent %d new control messages, want none", got-controlsBefore)
	}
}
