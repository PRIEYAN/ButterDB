package phase05

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	repairLog = "out-of-order op repaired its key alone"
	warmupJob = "2f8fad5b-d9cb-469f-a165-70867728950e"
	firstJob  = "3f8fad5b-d9cb-469f-a165-70867728950e"
	secondJob = "4f8fad5b-d9cb-469f-a165-70867728950e"
	otherJob  = "5f8fad5b-d9cb-469f-a165-70867728950e"
)

func (w *world) seedAt(channelID, msgID int64, text string) {
	w.t.Helper()
	if err := w.network.SeedHistory(channelID, tgclient.HistoryMessage{MsgID: msgID, Text: text}); err != nil {
		w.t.Fatalf("seed message %d: %v", msgID, err)
	}
}

func (w *world) projectLocally(channelID, msgID int64, text string) {
	w.t.Helper()
	outcome, err := w.projection.Projector.ProjectFromOp(context.Background(), w.db, projection.Observed{
		ChannelID: channelID, MsgID: msgID, MsgDate: baseTime + msgID, ActorUserID: primaryUser, RawText: text,
	})
	if err != nil || outcome.Kind != projection.OutcomeApplied {
		w.t.Fatalf("local projection of message %d = %+v, %v", msgID, outcome, err)
	}
}

func TestPerKeyOrderingRepairPreservesLaterWrites(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		lateMsgID   int64
		localMsgID  int64
		lateOp      wire.Op
		localOp     wire.Op
		wantObjects []string
		wantTombs   int
	}{
		{
			name:        "a later put survives an earlier put that arrives late",
			lateMsgID:   101,
			localMsgID:  103,
			lateOp:      putOp("report.txt", 100, firstJob),
			localOp:     putOp("report.txt", 102, secondJob),
			wantObjects: []string{"report.txt|103|102|" + secondJob},
		},
		{
			name:       "a later delete survives an earlier put that arrives late",
			lateMsgID:  201,
			localMsgID: 203,
			lateOp:     putOp("report.txt", 200, firstJob),
			localOp:    delOp("report.txt", secondJob),
			wantTombs:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			info, err := w.sync.Buckets.Create(ctx, bucket.Spec{Name: "photos", Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			channelID := info.ChannelID
			warmBody := w.sendExternal(channelID, format(t, wire.Op{Type: wire.OpBody, JobID: warmupJob, StoredSize: 100}))
			w.sendExternal(channelID, format(t, putOp("other.txt", warmBody, otherJob)))
			if err := w.sync.Difference.Sync(ctx, channelID); err != nil {
				t.Fatalf("warm-up Sync: %v", err)
			}
			if w.channelState(channelID).Pts == 0 {
				t.Fatal("the warm-up pass left no pts, so the difference path would not run")
			}
			untouched := w.rows(`SELECT channel_id, object_key, put_msg_id, body_msg_id, job_id, etag, msg_date FROM objects WHERE object_key = 'other.txt'`)

			w.seedAt(channelID, tc.lateMsgID, format(t, tc.lateOp))
			w.seedAt(channelID, tc.localMsgID, format(t, tc.localOp))
			w.projectLocally(channelID, tc.localMsgID, format(t, tc.localOp))

			if strings.Contains(w.logs.String(), repairLog) {
				t.Fatalf("the warm-up pass repaired a key: %s", w.logs.String())
			}
			if err := w.sync.Difference.Sync(ctx, channelID); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if got := strings.Count(w.logs.String(), repairLog); got != 1 {
				t.Fatalf("keys repaired by the pass = %d, want exactly the one out-of-order key", got)
			}

			reported := w.rows(`SELECT object_key, put_msg_id, body_msg_id, job_id FROM objects WHERE channel_id = ?1 AND object_key = 'report.txt'`, channelID)
			if !reflect.DeepEqual(reported, tc.wantObjects) {
				t.Fatalf("report.txt rows = %v, want %v", reported, tc.wantObjects)
			}
			if got := w.count(`SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ?1 AND object_key = 'report.txt'`, channelID); got != tc.wantTombs {
				t.Fatalf("tombstones for report.txt = %d, want %d", got, tc.wantTombs)
			}
			if got := w.rows(`SELECT channel_id, object_key, put_msg_id, body_msg_id, job_id, etag, msg_date FROM objects WHERE object_key = 'other.txt'`); !reflect.DeepEqual(got, untouched) {
				t.Fatalf("an unrelated key changed: %v, want %v", got, untouched)
			}
			if got := w.count(`SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1 AND msg_id = ?2`, channelID, tc.lateMsgID); got != 1 {
				t.Fatalf("cleanup rows for the late put = %d, want 1", got)
			}
			if got := w.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1 AND msg_id IN (?2, ?3)`, channelID, tc.lateMsgID, tc.localMsgID); got != 2 {
				t.Fatalf("replay log rows for the two ops = %d, want 2", got)
			}
			if state := w.channelState(channelID); state.NeedsProjectionRebuild {
				t.Fatalf("a single out-of-order op escalated to a whole rebuild: %+v", state)
			}
		})
	}
}
