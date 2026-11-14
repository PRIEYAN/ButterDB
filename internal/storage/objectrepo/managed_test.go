package objectrepo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestManagedMsgIDsInIncludesHiddenBodiesAndScopesCandidates(t *testing.T) {
	repo, db := newRepo(t)
	liveEncrypted := sampleMeta(testChannel, "live", 11)
	liveEncrypted.BodyMsgID = 10
	execAll(t, db,
		`INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (1001, 30, '`+testJobID+`', 5, 0, 0)`,
		`INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, writer, msg_date) VALUES (1001, 'gone', 60, '`+testJobID+`', 'w', 0)`,
		`INSERT INTO replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, is_document, msg_date)
		 VALUES (1001, 50, 'unknown', '{}', 'TDX1|t=unknown', '`+strings.Repeat("f", 64)+`', 0, 0),
		        (888888, 99, 'unknown', '{}', 'TDX1|t=unknown', '`+strings.Repeat("e", 64)+`', 0, 0)`,
		`INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (1001, 40, 'superseded'), (888888, 98, 'superseded')`,
	)
	putAll(t, db, liveEncrypted, sampleMeta(888888, "other", 99))

	ids, err := repo.ManagedMsgIDsIn(context.Background(), testChannel, []int64{10, 11, 30, 40, 50, 60, 98, 99, 100, 10})
	if err != nil {
		t.Fatalf("managed ids: %v", err)
	}
	got := slices.Sorted(maps.Keys(ids))
	want := []int64{10, 11, 30, 40, 50}
	if !slices.Equal(got, want) {
		t.Fatalf("managed ids = %v, want %v", got, want)
	}

	ids, err = repo.ManagedMsgIDsIn(context.Background(), testChannel, []int64{11})
	if err != nil || len(ids) != 1 {
		t.Fatalf("single candidate = %v, %v", ids, err)
	}
	if _, ok := ids[11]; !ok {
		t.Fatalf("single candidate returned unrelated ids: %v", ids)
	}
}

func TestManagedMsgIDsInEmptyAndInvalid(t *testing.T) {
	repo, _ := newRepo(t)
	ids, err := repo.ManagedMsgIDsIn(context.Background(), testChannel, nil)
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty candidates = %v, %v", ids, err)
	}
	if _, err := repo.ManagedMsgIDsIn(context.Background(), 0, []int64{1}); err == nil {
		t.Fatal("channel id 0 accepted")
	}
}

func TestManagedMsgIDsInBatchesAndHonorsCancellation(t *testing.T) {
	repo, db := newRepo(t)
	execAll(t, db, `INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (1001, 1500, 'orphan_body')`)
	candidates := make([]int64, 1500)
	for i := range candidates {
		candidates[i] = int64(i + 1)
	}
	ids, err := repo.ManagedMsgIDsIn(context.Background(), testChannel, candidates)
	if err != nil || len(ids) != 1 {
		t.Fatalf("batched ids = %v, %v", ids, err)
	}
	if _, ok := ids[1500]; !ok {
		t.Fatal("last batch omitted the owned message")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.ManagedMsgIDsIn(ctx, testChannel, candidates); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestManagedMessageLookupsUseIndexes(t *testing.T) {
	_, db := newRepo(t)
	wantIndex := map[string]string{
		"objects.put_msg_id":             "objects_put",
		"objects.body_msg_id":            "objects_body",
		"object_bodies.msg_id":           "sqlite_autoindex_object_bodies_1",
		"replay_log.msg_id":              "sqlite_autoindex_replay_log_1",
		"pending_message_cleanup.msg_id": "sqlite_autoindex_pending_message_cleanup_1",
	}
	for _, source := range managedSources {
		name := source.table + "." + source.column
		t.Run(name, func(t *testing.T) {
			statement := fmt.Sprintf("SELECT %[2]s FROM %[1]s WHERE channel_id = 1001 AND %[3]s%[2]s IN (10, 11)", source.table, source.column, source.guard)
			plan := strings.Join(explain(t, db, statement), "\n")
			if !strings.Contains(plan, wantIndex[name]) || strings.Contains(plan, "SCAN") {
				t.Fatalf("ownership lookup did not search %s:\n%s", wantIndex[name], plan)
			}
		})
	}
}
