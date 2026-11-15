package objectrepo

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func newQueue(t *testing.T) *CleanupQueue {
	t.Helper()
	_, db := newRepo(t)
	queue, err := NewCleanupQueue(db)
	if err != nil {
		t.Fatalf("new cleanup queue: %v", err)
	}
	return queue
}

func queueIDs(t *testing.T, q *CleanupQueue, channelID int64, ids ...int64) {
	t.Helper()
	err := q.db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := QueueCleanup(context.Background(), tx, Cleanup{ChannelID: channelID, MsgID: id, Reason: "superseded", ObjectKey: "k"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("queue ids: %v", err)
	}
}

func cleanupRow(t *testing.T, q *CleanupQueue, channelID, msgID int64) (attempts, next int64, lastError string, found bool) {
	t.Helper()
	err := q.db.Read(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRowContext(context.Background(), "SELECT attempts, next_attempt_at, last_error FROM pending_message_cleanup WHERE channel_id = ?1 AND msg_id = ?2", channelID, msgID).Scan(&attempts, &next, &lastError)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		t.Fatalf("read cleanup row: %v", err)
	}
	return
}

func TestNewCleanupQueueRejectsNil(t *testing.T) {
	if _, err := NewCleanupQueue(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("error = %v", err)
	}
}

func TestDueCleanupChannels(t *testing.T) {
	q := newQueue(t)
	queueIDs(t, q, 30, 1)
	queueIDs(t, q, 10, 1, 2)
	queueIDs(t, q, 20, 5)
	if err := q.DeferCleanup(context.Background(), 20, []int64{5}, 1000, "boom"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	tests := []struct {
		name  string
		now   int64
		limit int
		want  []int64
	}{
		{"deferred channel excluded", 0, 10, []int64{10, 30}},
		{"deferred channel due later", 1000 + 60000, 10, []int64{10, 20, 30}},
		{"limit applied", 1000 + 60000, 2, []int64{10, 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := q.DueCleanupChannels(context.Background(), tt.now, tt.limit)
			if err != nil {
				t.Fatalf("due: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("channels = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaimCleanupBatchDropsLiveObjectMessages(t *testing.T) {
	q := newQueue(t)
	live := sampleMeta(testChannel, "live", 11)
	err := q.db.Write(context.Background(), func(tx *sql.Tx) error { return PutObject(context.Background(), tx, live, 1) })
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	err = q.db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range []int64{10, 11, 7, 8} {
			if _, err := tx.ExecContext(context.Background(), "INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (?1, ?2, 'superseded')", testChannel, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed cleanup rows: %v", err)
	}
	got, err := q.ClaimCleanupBatch(context.Background(), testChannel, 0, 100)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{7, 8}) {
		t.Fatalf("claimed %v, want [7 8]", got)
	}
	for _, id := range []int64{10, 11} {
		if _, _, _, found := cleanupRow(t, q, testChannel, id); found {
			t.Fatalf("cleanup row for live message %d survived the claim", id)
		}
	}
}

func TestClaimCleanupBatchLimitsAndDueness(t *testing.T) {
	q := newQueue(t)
	ids := make([]int64, 150)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	queueIDs(t, q, testChannel, ids...)
	tests := []struct {
		name    string
		channel int64
		now     int64
		limit   int
		want    int
	}{
		{"capped at one hundred", testChannel, 0, 500, 100},
		{"smaller limit honoured", testChannel, 0, 7, 7},
		{"zero limit", testChannel, 0, 0, 0},
		{"other channel empty", testChannel + 1, 0, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := q.ClaimCleanupBatch(context.Background(), tt.channel, tt.now, tt.limit)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("claimed %d, want %d", len(got), tt.want)
			}
		})
	}
	if err := q.DeferCleanup(context.Background(), testChannel, ids, 500, "x"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	got, err := q.ClaimCleanupBatch(context.Background(), testChannel, 499, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("claim before next attempt = %v, %v", got, err)
	}
}

func TestConfirmCleanupDeleted(t *testing.T) {
	q := newQueue(t)
	queueIDs(t, q, testChannel, 1, 2, 3)
	if err := q.ConfirmCleanupDeleted(context.Background(), testChannel, []int64{1, 2}, 1234); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := q.ConfirmCleanupDeleted(context.Background(), testChannel, []int64{2, 9}, 5678); err != nil {
		t.Fatalf("confirm again: %v", err)
	}
	if _, _, _, found := cleanupRow(t, q, testChannel, 3); !found {
		t.Fatal("unconfirmed row removed")
	}
	for _, id := range []int64{1, 2} {
		if _, _, _, found := cleanupRow(t, q, testChannel, id); found {
			t.Fatalf("confirmed row %d still queued", id)
		}
	}
	recorded := map[int64]int64{}
	err := q.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), "SELECT msg_id, recorded_at FROM deleted_messages WHERE channel_id = ?1 AND source = 'cleanup'", testChannel)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, at int64
			if err := rows.Scan(&id, &at); err != nil {
				return err
			}
			recorded[id] = at
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read deleted: %v", err)
	}
	want := map[int64]int64{1: 1234, 2: 1234, 9: 5678}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("deleted_messages = %v, want %v", recorded, want)
	}
}

func TestDeferCleanupBackoff(t *testing.T) {
	q := newQueue(t)
	queueIDs(t, q, testChannel, 1)
	const base = int64(1_000_000)
	minute := int64(60_000)
	wants := []int64{minute, 2 * minute, 4 * minute, 8 * minute, 16 * minute, 32 * minute, 60 * minute, 60 * minute}
	for i, want := range wants {
		if err := q.DeferCleanup(context.Background(), testChannel, []int64{1}, base, "flood"); err != nil {
			t.Fatalf("defer %d: %v", i, err)
		}
		attempts, next, lastError, _ := cleanupRow(t, q, testChannel, 1)
		if attempts != int64(i+1) || next != base+want || lastError != "flood" {
			t.Fatalf("after defer %d: attempts=%d next=%d error=%q; want next %d", i+1, attempts, next, lastError, base+want)
		}
	}
}

func TestDeferCleanupHugeAttemptCountStaysCapped(t *testing.T) {
	q := newQueue(t)
	queueIDs(t, q, testChannel, 1)
	err := q.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "UPDATE pending_message_cleanup SET attempts = 900")
		return err
	})
	if err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
	if err := q.DeferCleanup(context.Background(), testChannel, []int64{1}, 10, strings.Repeat("e", 3)); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if _, next, _, _ := cleanupRow(t, q, testChannel, 1); next != 10+60*60*1000 {
		t.Fatalf("next = %d, want the one hour cap", next)
	}
}
