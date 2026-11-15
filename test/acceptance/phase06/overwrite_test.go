package phase06

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

var errDeleteRefused = errors.New("delete refused")

func TestOverwriteCleansSupersededMessages(t *testing.T) {
	t.Run("an overwrite deletes the superseded put and body only after the new put is projected", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		first := w.mustPut("photos", "doc.txt", []byte("version one"))
		second := w.mustPut("photos", "doc.txt", []byte("version two is longer"))
		if second.PutMsgID <= first.PutMsgID {
			t.Fatalf("second put message %d is not above the first %d", second.PutMsgID, first.PutMsgID)
		}
		ids := w.messageIDs(info.ChannelID)
		if ids[first.PutMsgID] || ids[first.BodyMsgID] {
			t.Fatalf("superseded messages %d and %d still exist in Telegram", first.PutMsgID, first.BodyMsgID)
		}
		if !ids[second.PutMsgID] || !ids[second.BodyMsgID] {
			t.Fatalf("live messages %d and %d are missing from Telegram", second.PutMsgID, second.BodyMsgID)
		}
		if got := w.projected(`SELECT put_msg_id, body_msg_id FROM objects WHERE channel_id = ?1 AND object_key = 'doc.txt'`, info.ChannelID); len(got) != 1 || got[0] != itoa(second.PutMsgID)+"|"+itoa(second.BodyMsgID) {
			t.Fatalf("object row = %v, want the second version", got)
		}
		if got := w.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("pending cleanup rows = %d, want 0", got)
		}
		confirmed := w.projected(`SELECT msg_id FROM deleted_messages WHERE channel_id = ?1 AND source = 'cleanup' ORDER BY msg_id`, info.ChannelID)
		if len(confirmed) != 2 {
			t.Fatalf("confirmed cleanup rows = %v, want the superseded put and body", confirmed)
		}
	})

	t.Run("a cleanup that fails stays queued and the sweeper finishes it", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		first := w.mustPut("photos", "doc.txt", []byte("version one"))
		if err := w.client.QueueFaults(faketg.MethodDeleteMessages, faketg.Fault{Kind: faketg.FaultError, Err: errDeleteRefused}); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		second := w.mustPut("photos", "doc.txt", []byte("version two"))
		ids := w.messageIDs(info.ChannelID)
		if !ids[first.PutMsgID] || !ids[first.BodyMsgID] {
			t.Fatal("superseded messages were deleted although the batch failed")
		}
		if got := w.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1`, info.ChannelID); got != 2 {
			t.Fatalf("pending cleanup rows = %d, want the superseded put and body", got)
		}
		w.clk.Advance(2 * time.Hour)
		w.write.Sweeper.RunOnce(context.Background())
		ids = w.messageIDs(info.ChannelID)
		if ids[first.PutMsgID] || ids[first.BodyMsgID] || !ids[second.PutMsgID] || !ids[second.BodyMsgID] {
			t.Fatalf("messages after the sweeper pass = %v", ids)
		}
		if got := w.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("pending cleanup rows after the sweeper = %d, want 0", got)
		}
	})
}
