package channelrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const testJobID = "00000000-0000-4000-8000-000000000000"

func newRepo(t *testing.T) (*Repository, *sqlitedb.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repo, db
}

func execAll(t *testing.T, db *sqlitedb.DB, statements ...string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(context.Background(), statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func countRows(t *testing.T, db *sqlitedb.DB, query string, args ...any) int {
	t.Helper()
	var count int
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&count)
	})
	if err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func mustGet(t *testing.T, repo *Repository, channelID int64) Channel {
	t.Helper()
	channel, found, err := repo.GetChannel(context.Background(), channelID)
	if err != nil || !found {
		t.Fatalf("get channel %d: found=%v err=%v", channelID, found, err)
	}
	return channel
}

func seedChannel(t *testing.T, repo *Repository, channelID int64, kind Kind) {
	t.Helper()
	if err := repo.UpsertChannel(context.Background(), Channel{ChannelID: channelID, AccessHash: 1, Kind: kind, Title: "seed"}); err != nil {
		t.Fatalf("seed channel %d: %v", channelID, err)
	}
}

func TestNewRejectsNilSource(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("error = %v, want ErrMissingDependency", err)
	}
}

func TestInsertChannelInsertsAndUpdates(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	channel := Channel{ChannelID: 5555, AccessHash: 777, Title: "butterDB bucket photos", Kind: KindBucket}
	if err := repo.UpsertChannel(ctx, channel); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	got := mustGet(t, repo, 5555)
	if got.Title != "butterDB bucket photos" || got.AccessHash != 777 || got.Kind != KindBucket || got.Deleted {
		t.Fatalf("got %+v", got)
	}

	execAll(t, db, `UPDATE channels SET last_synced_msg = 40, pts = 9, initial_sync_done = 1, needs_projection_rebuild = 1, last_reconciled_at = 5 WHERE channel_id = 5555`)
	channel.AccessHash = 888
	channel.Title = "renamed"
	channel.Kind = KindRegistry
	if err := repo.UpsertChannel(ctx, channel); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	got = mustGet(t, repo, 5555)
	want := Channel{ChannelID: 5555, AccessHash: 888, Kind: KindBucket, Title: "renamed", LastSyncedMsg: 40, Pts: 9,
		InitialSyncDone: true, NeedsProjectionRebuild: true, LastReconciledAt: 5}
	if got != want {
		t.Fatalf("upsert result = %+v, want %+v", got, want)
	}
}

func TestInsertChannelRejectsBadInput(t *testing.T) {
	repo, _ := newRepo(t)
	tests := []struct {
		name    string
		channel Channel
	}{
		{"zero id", Channel{AccessHash: 1, Kind: KindBucket}},
		{"negative id", Channel{ChannelID: -4, AccessHash: 1, Kind: KindBucket}},
		{"bad kind", Channel{ChannelID: 1, AccessHash: 1, Kind: "wat"}},
		{"zero access hash", Channel{ChannelID: 1, Kind: KindBucket}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.UpsertChannel(context.Background(), tt.channel)
			if !errors.Is(err, ErrInvalidChannel) {
				t.Fatalf("error = %v, want ErrInvalidChannel", err)
			}
		})
	}
	channels, err := repo.ListChannels(context.Background())
	if err != nil || len(channels) != 0 {
		t.Fatalf("channels = %v, err = %v, want none", channels, err)
	}
}

func TestUpdateAccessHash(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedChannel(t, repo, 10, KindBucket)
	if err := repo.UpdateAccessHash(ctx, 10, 4242); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := mustGet(t, repo, 10).AccessHash; got != 4242 {
		t.Fatalf("access hash = %d, want 4242", got)
	}
	if err := repo.UpdateAccessHash(ctx, 10, 0); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("zero hash error = %v, want ErrInvalidChannel", err)
	}
	if err := repo.UpdateAccessHash(ctx, 99, 5); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("unknown channel error = %v, want ErrChannelNotFound", err)
	}
	if got := mustGet(t, repo, 10).AccessHash; got != 4242 {
		t.Fatalf("access hash after refusals = %d, want 4242", got)
	}
}

func TestGetAndListChannels(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	if _, found, err := repo.GetChannel(ctx, 1); err != nil || found {
		t.Fatalf("missing channel: found=%v err=%v", found, err)
	}
	seedChannel(t, repo, 30, KindBucket)
	seedChannel(t, repo, 20, KindBucket)
	seedChannel(t, repo, 40, KindRegistry)
	channels, err := repo.ListChannels(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var ids []int64
	for _, channel := range channels {
		ids = append(ids, channel.ChannelID)
	}
	if len(ids) != 3 || ids[0] != 40 || ids[1] != 20 || ids[2] != 30 {
		t.Fatalf("order = %v, want [40 20 30]", ids)
	}
	if _, err := repo.ChannelExists(ctx, 0); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("exists zero id error = %v", err)
	}
	if exists, err := repo.ChannelExists(ctx, 20); err != nil || !exists {
		t.Fatalf("exists 20 = %v, %v", exists, err)
	}
	if exists, err := repo.ChannelExists(ctx, 21); err != nil || exists {
		t.Fatalf("exists 21 = %v, %v", exists, err)
	}
}

func TestSetChannelPtsRoundTripsThroughGetAndListChannels(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedChannel(t, repo, 3333, KindBucket)
	err := db.Write(ctx, func(tx *sql.Tx) error {
		return repo.SetSyncCursorTx(ctx, tx, 3333, 0, 424242)
	})
	if err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	if got := mustGet(t, repo, 3333); got.Pts != 424242 {
		t.Fatalf("pts = %d, want 424242", got.Pts)
	}
	channels, err := repo.ListChannels(ctx)
	if err != nil || len(channels) != 1 || channels[0].Pts != 424242 {
		t.Fatalf("list = %+v, err = %v", channels, err)
	}
	err = db.Write(ctx, func(tx *sql.Tx) error {
		return repo.SetSyncCursorTx(ctx, tx, 3333, 0, 999)
	})
	if err != nil {
		t.Fatalf("set cursor again: %v", err)
	}
	if got := mustGet(t, repo, 3333).Pts; got != 999 {
		t.Fatalf("pts after second write = %d, want 999", got)
	}
}

func TestPtsAndWatermarkInCallerTx(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedChannel(t, repo, 7, KindBucket)

	rollback := errors.New("rollback")
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if err := repo.SetSyncCursorTx(ctx, tx, 7, 50, 12); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("write error = %v, want rollback", err)
	}
	if got := mustGet(t, repo, 7); got.LastSyncedMsg != 0 || got.Pts != 0 {
		t.Fatalf("rolled back cursor leaked: %+v", got)
	}

	err = db.Write(ctx, func(tx *sql.Tx) error {
		return repo.SetSyncCursorTx(ctx, tx, 7, 50, 12)
	})
	if err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	got := mustGet(t, repo, 7)
	if got.LastSyncedMsg != 50 || got.Pts != 12 || got.InitialSyncDone || got.NeedsProjectionRebuild {
		t.Fatalf("after cursor = %+v", got)
	}

	err = db.Write(ctx, func(tx *sql.Tx) error {
		return repo.SetSyncCursorTx(ctx, tx, 7, 30, 15)
	})
	if err != nil {
		t.Fatalf("set lower watermark: %v", err)
	}
	if got := mustGet(t, repo, 7); got.LastSyncedMsg != 50 || got.Pts != 15 {
		t.Fatalf("watermark moved backwards or pts lost: %+v", got)
	}

	err = db.Write(ctx, func(tx *sql.Tx) error {
		return repo.MarkInitialSyncDoneTx(ctx, tx, 7, 80, 20)
	})
	if err != nil {
		t.Fatalf("mark initial sync done: %v", err)
	}
	got = mustGet(t, repo, 7)
	if !got.InitialSyncDone || got.LastSyncedMsg != 80 || got.Pts != 20 || got.NeedsProjectionRebuild {
		t.Fatalf("after initial sync = %+v", got)
	}

	if err := repo.SetNeedsProjectionRebuild(ctx, 7, true); err != nil {
		t.Fatalf("set rebuild: %v", err)
	}
	if err := repo.SetLastReconciledAt(ctx, 7, 1234); err != nil {
		t.Fatalf("set reconciled: %v", err)
	}
	got = mustGet(t, repo, 7)
	if !got.NeedsProjectionRebuild || got.LastReconciledAt != 1234 || !got.InitialSyncDone || got.LastSyncedMsg != 80 || got.Pts != 20 {
		t.Fatalf("after flags = %+v", got)
	}
	if err := repo.SetNeedsProjectionRebuild(ctx, 7, false); err != nil {
		t.Fatalf("clear rebuild: %v", err)
	}
	if got := mustGet(t, repo, 7); got.NeedsProjectionRebuild {
		t.Fatalf("rebuild flag not cleared: %+v", got)
	}
}

func TestCursorRefusals(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedChannel(t, repo, 7, KindBucket)
	tests := []struct {
		name      string
		channelID int64
		msg       int64
		pts       int
		want      error
	}{
		{"unknown channel", 99, 1, 1, ErrChannelNotFound},
		{"negative watermark", 7, -1, 1, ErrInvalidChannel},
		{"negative pts", 7, 1, -1, ErrInvalidChannel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.Write(ctx, func(tx *sql.Tx) error {
				return repo.SetSyncCursorTx(ctx, tx, tt.channelID, tt.msg, tt.pts)
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			err = db.Write(ctx, func(tx *sql.Tx) error {
				return repo.MarkInitialSyncDoneTx(ctx, tx, tt.channelID, tt.msg, tt.pts)
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("mark error = %v, want %v", err, tt.want)
			}
		})
	}
	if err := repo.SetNeedsProjectionRebuild(ctx, 99, true); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("rebuild unknown channel error = %v", err)
	}
	if err := repo.SetLastReconciledAt(ctx, 99, 1); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("reconciled unknown channel error = %v", err)
	}
}

func TestSpoolBatchesAndClear(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	const total = 600
	msgs := make([]SpoolMessage, 0, total)
	for id := int64(total); id >= 1; id-- {
		msg := SpoolMessage{MsgID: id, Date: 1700000000 + id, FromID: 9, Text: fmt.Sprintf("t=put %d", id)}
		if id%3 == 0 {
			msg.HasMedia = true
			msg.MediaSize = id * 10
			msg.DocumentName = fmt.Sprintf("doc-%d", id)
		}
		msgs = append(msgs, msg)
	}
	if err := repo.SpoolPage(ctx, 5, msgs); err != nil {
		t.Fatalf("spool page: %v", err)
	}
	if err := repo.SpoolPage(ctx, 6, msgs[:3]); err != nil {
		t.Fatalf("spool other channel: %v", err)
	}
	if err := repo.SpoolPage(ctx, 5, nil); err != nil {
		t.Fatalf("spool empty page: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = 5`); got != total {
		t.Fatalf("spooled rows = %d, want %d", got, total)
	}

	var all []SpoolMessage
	after := int64(0)
	for {
		var page []SpoolMessage
		err := db.Read(ctx, func(tx *sql.Tx) error {
			var err error
			page, err = repo.SpooledPageTx(ctx, tx, 5, after, 250)
			return err
		})
		if err != nil {
			t.Fatalf("read page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		after = page[len(page)-1].MsgID
	}
	if len(all) != total {
		t.Fatalf("read %d rows, want %d", len(all), total)
	}
	for i, msg := range all {
		if msg.MsgID != int64(i+1) {
			t.Fatalf("row %d has msg id %d, want ascending", i, msg.MsgID)
		}
	}
	if all[2].Text != "t=put 3" || !all[2].HasMedia || all[2].MediaSize != 30 || all[2].DocumentName != "doc-3" || all[2].FromID != 9 || all[2].Date != 1700000003 {
		t.Fatalf("media row = %+v", all[2])
	}
	if all[0].HasMedia || all[0].MediaSize != 0 || all[0].DocumentName != "" {
		t.Fatalf("plain row = %+v", all[0])
	}

	err := db.Read(ctx, func(tx *sql.Tx) error {
		_, err := repo.SpooledPageTx(ctx, tx, 5, 0, 0)
		return err
	})
	if !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("zero limit error = %v, want ErrInvalidChannel", err)
	}

	if err := repo.SpoolPage(ctx, 5, msgs[:1]); err != nil {
		t.Fatalf("respool: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = 5`); got != total {
		t.Fatalf("rows after respool = %d, want %d", got, total)
	}

	if err := repo.ClearSpool(ctx, 5); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = 5`); got != 0 {
		t.Fatalf("rows after clear = %d, want 0", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = 6`); got != 3 {
		t.Fatalf("other channel rows = %d, want 3", got)
	}
}

func insertBucketRow(channelID int64, objects, stored, plaintext int64) string {
	return fmt.Sprintf(`INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id,
  created_job_id, created_date, config_msg_id, object_count, stored_bytes, plaintext_bytes)
VALUES (%d, 'bucket-%d', 'private', 'none', 0, 1, '%s', 1, 1, %d, %d, %d)`, channelID, channelID, testJobID, objects, stored, plaintext)
}

func insertObjectRow(channelID int64, key string, putMsgID, bodyMsgID, stored int64) string {
	return fmt.Sprintf(`INSERT INTO objects (channel_id, object_key, put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256,
  content_type, encrypted, writer, op_ts, msg_date)
VALUES (%d, '%s', %d, %d, '%s', %d, %d, '%032d', '%064d', 'text/plain', 0, 'w', 1, 1)`,
		channelID, key, putMsgID, bodyMsgID, testJobID, stored, stored, 0, 0)
}

func TestDeletedMessagesRecorded(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	execAll(t, db,
		insertBucketRow(5, 4, 600, 600),
		insertBucketRow(6, 1, 50, 50),
		insertObjectRow(5, "a", 12, 11, 100),
		insertObjectRow(5, "b", 22, 21, 200),
		insertObjectRow(5, "c", 32, 31, 300),
		insertObjectRow(5, "empty", 40, 0, 0),
		insertObjectRow(6, "other", 12, 11, 50),
		`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (5, 21, 'cleanup', 100)`,
	)

	var removed []string
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		removed, err = repo.RecordDeletedTx(ctx, tx, 5, []int64{11, 22, 21, 40, 999}, DeletedExternal, 200)
		return err
	})
	if err != nil {
		t.Fatalf("record deleted: %v", err)
	}
	if len(removed) != 3 || removed[0] != "a" || removed[1] != "b" || removed[2] != "empty" {
		t.Fatalf("removed = %v, want [a b empty]", removed)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM objects WHERE channel_id = 5`); got != 1 {
		t.Fatalf("live objects = %d, want 1", got)
	}
	var objects, stored, plaintext int64
	err = db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT object_count, stored_bytes, plaintext_bytes FROM buckets WHERE channel_id = 5`).Scan(&objects, &stored, &plaintext)
	})
	if err != nil || objects != 1 || stored != 300 || plaintext != 300 {
		t.Fatalf("counters = %d %d %d err=%v, want 1 300 300", objects, stored, plaintext, err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM objects WHERE channel_id = 6`); got != 1 {
		t.Fatalf("other channel objects = %d, want 1", got)
	}
	if got := countRows(t, db, `SELECT object_count FROM buckets WHERE channel_id = 6`); got != 1 {
		t.Fatalf("other channel counter = %d, want 1", got)
	}

	sources := map[int64]string{}
	err = db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT msg_id, source FROM deleted_messages WHERE channel_id = 5`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var msgID int64
			var source string
			if err := rows.Scan(&msgID, &source); err != nil {
				return err
			}
			sources[msgID] = source
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read deleted: %v", err)
	}
	want := map[int64]string{11: "external", 22: "external", 21: "cleanup", 40: "external", 999: "external"}
	if len(sources) != len(want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
	for msgID, source := range want {
		if sources[msgID] != source {
			t.Fatalf("source of %d = %q, want %q", msgID, sources[msgID], source)
		}
	}
}

func TestDeletedMessagesRefusals(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		msgIDs []int64
		source DeletionSource
	}{
		{"bad source", []int64{1}, "wat"},
		{"zero message id", []int64{0}, DeletedCleanup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.Write(ctx, func(tx *sql.Tx) error {
				_, err := repo.RecordDeletedTx(ctx, tx, 5, tt.msgIDs, tt.source, 1)
				return err
			})
			if !errors.Is(err, ErrInvalidChannel) {
				t.Fatalf("error = %v, want ErrInvalidChannel", err)
			}
		})
	}
	err := db.Write(ctx, func(tx *sql.Tx) error {
		keys, err := repo.RecordDeletedTx(ctx, tx, 5, nil, DeletedReconcile, 1)
		if len(keys) != 0 {
			t.Fatalf("keys = %v, want none", keys)
		}
		return err
	})
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
}

func TestDeletedMessagesLargeList(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	statements := []string{insertBucketRow(5, 3, 3, 3)}
	for i := int64(1); i <= 3; i++ {
		statements = append(statements, insertObjectRow(5, fmt.Sprintf("k%d", i), i*1000+2, i*1000+1, 1))
	}
	execAll(t, db, statements...)
	ids := []int64{1001}
	for id := int64(5000); id < 5600; id++ {
		ids = append(ids, id)
	}
	ids = append(ids, 2002, 3001)
	var removed []string
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		removed, err = repo.RecordDeletedTx(ctx, tx, 5, ids, DeletedReconcile, 1)
		return err
	})
	if err != nil {
		t.Fatalf("record deleted: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed = %v, want 3 keys", removed)
	}
	if got := countRows(t, db, `SELECT object_count FROM buckets WHERE channel_id = 5`); got != 0 {
		t.Fatalf("object_count = %d, want 0", got)
	}
}

func seedScoped(t *testing.T, db *sqlitedb.DB, channelID int64) {
	t.Helper()
	execAll(t, db,
		insertBucketRow(channelID, 1, 10, 10),
		insertObjectRow(channelID, "k", 3, 2, 10),
		fmt.Sprintf(`INSERT INTO replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, is_document, msg_date)
VALUES (%d, 3, 'put', '{}', 'h', '%064d', 0, 1)`, channelID, 0),
		fmt.Sprintf(`INSERT INTO replay_log_tamper (channel_id, msg_id, old_hash, new_hash, new_raw_header)
VALUES (%d, 3, '%064d', '%064d', 'h')`, channelID, 0, 1),
		fmt.Sprintf(`INSERT INTO replay_log_rejects (channel_id, msg_id, code, raw_header, msg_date) VALUES (%d, 4, 'unknown_op', 'h', 1)`, channelID),
		fmt.Sprintf(`INSERT INTO registry_markers (channel_id, msg_id, deployment_id, format_version, min_reader, kek_fingerprint, primary_user_id, msg_date)
VALUES (%d, 1, '%026d', 1, 1, 'bkf1_%032d', 1, 1)`, channelID, 0, 0),
		fmt.Sprintf(`INSERT INTO object_tombstones (channel_id, object_key, del_msg_id, job_id, writer, msg_date) VALUES (%d, 'gone', 6, '%s', 'w', 1)`, channelID, testJobID),
		fmt.Sprintf(`INSERT INTO object_bodies (channel_id, msg_id, job_id, stored_size, encrypted, msg_date) VALUES (%d, 2, '%s', 10, 0, 1)`, channelID, testJobID),
		fmt.Sprintf(`INSERT INTO encryption_keys (bucket_channel_id, key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id, job_id, msg_date)
VALUES (%d, '%016d', zeroblob(72), zeroblob(61), 'bkf1_%032d', 1, 1, 1, '%s', 1)`, channelID, 0, 0, testJobID),
		fmt.Sprintf(`INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (%d, '%s', 'put', 3, 'applied')`, channelID, testJobID),
		fmt.Sprintf(`INSERT INTO initial_scan_spool (channel_id, msg_id, from_id, date, text, has_media, media_size, document_name) VALUES (%d, 3, 1, 1, 't', 0, 0, '')`, channelID),
		fmt.Sprintf(`INSERT INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (%d, 8, 'cleanup', 1)`, channelID),
		fmt.Sprintf(`INSERT INTO pending_message_cleanup (channel_id, msg_id, reason) VALUES (%d, 9, 'superseded')`, channelID),
		fmt.Sprintf(`INSERT INTO pending_channel_cleanup (channel_id, bdel_msg_id) VALUES (%d, 10)`, channelID),
	)
}

var scopedCounts = map[string]string{
	"channels":                `SELECT COUNT(*) FROM channels WHERE channel_id = ?1`,
	"buckets":                 `SELECT COUNT(*) FROM buckets WHERE channel_id = ?1`,
	"objects":                 `SELECT COUNT(*) FROM objects WHERE channel_id = ?1`,
	"replay_log":              `SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1`,
	"replay_log_tamper":       `SELECT COUNT(*) FROM replay_log_tamper WHERE channel_id = ?1`,
	"replay_log_rejects":      `SELECT COUNT(*) FROM replay_log_rejects WHERE channel_id = ?1`,
	"registry_markers":        `SELECT COUNT(*) FROM registry_markers WHERE channel_id = ?1`,
	"object_tombstones":       `SELECT COUNT(*) FROM object_tombstones WHERE channel_id = ?1`,
	"object_bodies":           `SELECT COUNT(*) FROM object_bodies WHERE channel_id = ?1`,
	"encryption_keys":         `SELECT COUNT(*) FROM encryption_keys WHERE bucket_channel_id = ?1`,
	"projection_operations":   `SELECT COUNT(*) FROM projection_operations WHERE channel_id = ?1`,
	"initial_scan_spool":      `SELECT COUNT(*) FROM initial_scan_spool WHERE channel_id = ?1`,
	"deleted_messages":        `SELECT COUNT(*) FROM deleted_messages WHERE channel_id = ?1`,
	"pending_message_cleanup": `SELECT COUNT(*) FROM pending_message_cleanup WHERE channel_id = ?1`,
	"pending_channel_cleanup": `SELECT COUNT(*) FROM pending_channel_cleanup WHERE channel_id = ?1`,
}

func TestDeleteChannelCascadesAllScopedTables(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	const doomed, survivor int64 = 9999, 8888
	seedChannel(t, repo, doomed, KindBucket)
	seedChannel(t, repo, survivor, KindBucket)
	seedScoped(t, db, doomed)
	seedScoped(t, db, survivor)
	for table, query := range scopedCounts {
		if got := countRows(t, db, query, doomed); got != 1 {
			t.Fatalf("seed %s rows = %d, want 1", table, got)
		}
	}

	if err := repo.DeleteChannel(ctx, doomed); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for table, query := range scopedCounts {
		if got := countRows(t, db, query, doomed); got != 0 {
			t.Fatalf("%s rows after delete = %d, want 0", table, got)
		}
		if got := countRows(t, db, query, survivor); got != 1 {
			t.Fatalf("%s survivor rows = %d, want 1", table, got)
		}
	}
	if err := repo.DeleteChannel(ctx, 0); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("zero id error = %v, want ErrInvalidChannel", err)
	}
}

func TestCompleteChannelCleanup(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	const gone, other int64 = 9999, 8888
	seedChannel(t, repo, gone, KindBucket)
	seedChannel(t, repo, other, KindBucket)
	seedScoped(t, db, gone)
	seedScoped(t, db, other)

	if err := repo.CompleteChannelCleanup(ctx, gone); err != nil {
		t.Fatalf("complete cleanup: %v", err)
	}
	removed := map[string]bool{
		"replay_log": true, "replay_log_rejects": true, "replay_log_tamper": true, "projection_operations": true,
		"deleted_messages": true, "initial_scan_spool": true, "pending_channel_cleanup": true,
	}
	for table, query := range scopedCounts {
		want := 1
		if removed[table] {
			want = 0
		}
		if got := countRows(t, db, query, gone); got != want {
			t.Fatalf("%s rows after cleanup = %d, want %d", table, got, want)
		}
		if got := countRows(t, db, query, other); got != 1 {
			t.Fatalf("%s other-channel rows = %d, want 1", table, got)
		}
	}
	if err := repo.CompleteChannelCleanup(ctx, 0); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("zero id error = %v, want ErrInvalidChannel", err)
	}
}
