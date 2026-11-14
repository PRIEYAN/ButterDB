package objectrepo

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

func TestLiveMessageIDsEmptyChannel(t *testing.T) {
	repo, _ := newRepo(t)
	refs, err := repo.LiveMessageIDs(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("live message ids: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want empty", refs)
	}
}

func TestLiveMessageIDsSingleObject(t *testing.T) {
	repo, db := newRepo(t)
	withBody := sampleMeta(testChannel, "x.png", 10)
	withBody.BodyMsgID = 9
	zeroByte := sampleMeta(testChannel, "empty", 20)
	zeroByte.BodyMsgID = 0
	zeroByte.StoredSize = 0
	zeroByte.PlaintextSize = 0
	putAll(t, db, withBody, zeroByte, sampleMeta(testChannel+1, "other", 30))

	refs, err := repo.LiveMessageIDs(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("live message ids: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2", refs)
	}
	if refs[0].Key != "x.png" || refs[0].PutMsgID != 10 || !slices.Equal(refs[0].BackingMsgIDs, []int64{10, 9}) {
		t.Fatalf("refs[0] = %+v, want put 10 backed by [10 9]", refs[0])
	}
	if refs[1].Key != "empty" || refs[1].PutMsgID != 20 || !slices.Equal(refs[1].BackingMsgIDs, []int64{20}) {
		t.Fatalf("refs[1] = %+v, want put 20 backed by [20]", refs[1])
	}
}

func TestLiveMessageIDsExcludesTombstoned(t *testing.T) {
	repo, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel, "deleted", 10), sampleMeta(testChannel, "resurrected", 30), sampleMeta(testChannel, "kept", 50))
	tombstone := func(key string, delMsgID int) string {
		return fmt.Sprintf(`INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, writer, msg_date) VALUES (1001, '%s', %d, '%s', 'w', 0)`, key, delMsgID, testJobID)
	}
	execAll(t, db, tombstone("deleted", 11), tombstone("resurrected", 20))

	refs, err := repo.LiveMessageIDs(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("live message ids: %v", err)
	}
	var keys []string
	for _, ref := range refs {
		keys = append(keys, ref.Key)
	}
	if want := []string{"resurrected", "kept"}; !slices.Equal(keys, want) {
		t.Fatalf("live keys = %q, want %q", keys, want)
	}
}

func TestLiveMessageIDsAfterDeleteWithTombstone(t *testing.T) {
	repo, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel, "x", 10))
	execAll(t, db,
		"DELETE FROM objects WHERE channel_id = 1001",
		`INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, writer, msg_date) VALUES (1001, 'x', 11, '`+testJobID+`', 'w', 0)`,
	)
	refs, err := repo.LiveMessageIDs(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("live message ids: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want empty after delete", refs)
	}
}
