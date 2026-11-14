package rebuild

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	registryChannel int64 = 1000
	bucketChannel   int64 = 2000
	otherChannel    int64 = 3000
)

var derivedTables = []string{
	"registry_markers", "buckets", "objects", "object_tombstones", "object_bodies",
	"encryption_keys", "projection_operations", "replay_log_rejects",
	"pending_message_cleanup", "replay_log", "replay_log_tamper", "deleted_messages",
}

type env struct {
	t       *testing.T
	db      *sqlitedb.DB
	applier *fakeRegistry
	rebuild *Rebuilder
}

func newEnv(t *testing.T) *env {
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
	registry := newFakeRegistry()
	rebuilder, err := New(Dependencies{Registry: registry, Logger: logger})
	if err != nil {
		t.Fatalf("new rebuilder: %v", err)
	}
	e := &env{t: t, db: db, applier: registry, rebuild: rebuilder}
	e.addChannel(registryChannel, "registry", "active")
	e.addChannel(bucketChannel, "bucket", "active")
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Reader().Exec(query, args...); err == nil {
		e.t.Fatalf("read pool accepted a write: %s", query)
	}
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) addChannel(id int64, kind, state string) {
	e.t.Helper()
	e.exec(`INSERT INTO channels (channel_id, kind, state, needs_projection_rebuild) VALUES (?, ?, ?, 1)`, id, kind, state)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) write(fn func(tx *sql.Tx) error) {
	e.t.Helper()
	if err := e.db.Write(context.Background(), fn); err != nil {
		e.t.Fatalf("write: %v", err)
	}
}

func headerOf(t *testing.T, op wire.Op) string {
	t.Helper()
	header, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return header
}

func jobID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n)
}

func shaOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (e *env) appendReplay(channelID, msgID int64, rawHeader string, isDocument bool) {
	e.t.Helper()
	op, err := wire.Parse(rawHeader)
	opType := string(op.Type)
	if err != nil && opType == "" {
		e.t.Fatalf("parse seed header: %v", err)
	}
	key, hasKey := wire.KeyOf(op)
	var objectKey any
	if hasKey {
		objectKey = key
	}
	document := 0
	if isDocument {
		document = 1
	}
	e.exec(`
		INSERT INTO replay_log (channel_id, msg_id, op_type, op_payload_json, raw_header, first_seen_hash, object_key, job_id, is_document, actor_user_id, msg_date)
		VALUES (?, ?, ?, '{}', ?, ?, ?, ?, ?, 0, ?)
	`, channelID, msgID, opType, rawHeader, shaOf(rawHeader), objectKey, op.JobID, document, 1000+msgID)
}

func (e *env) project(channelID, msgID int64, rawHeader string, isDocument bool) {
	e.t.Helper()
	e.appendReplay(channelID, msgID, rawHeader, isDocument)
	e.applyLogged(channelID, msgID)
}

func (e *env) applyLogged(channelID, msgID int64) {
	e.t.Helper()
	e.write(func(tx *sql.Tx) error {
		row := replayRow{msgID: msgID}
		var document int
		err := tx.QueryRow(`SELECT op_type, raw_header, is_document, actor_user_id, msg_date FROM replay_log WHERE channel_id = ? AND msg_id = ?`, channelID, msgID).
			Scan(&row.opType, &row.rawHeader, &document, &row.actorUserID, &row.msgDate)
		if err != nil {
			return err
		}
		row.isDocument = document != 0
		_, err = e.rebuild.applyRow(context.Background(), tx, channelID, row)
		return err
	})
}

func (e *env) rebuildAll() Result {
	e.t.Helper()
	result, err := e.rebuild.RebuildAll(context.Background(), e.db)
	if err != nil {
		e.t.Fatalf("rebuild all: %v", err)
	}
	return result
}

func (e *env) snapshot(tables ...string) map[string][]string {
	e.t.Helper()
	if len(tables) == 0 {
		tables = derivedTables
	}
	out := make(map[string][]string, len(tables))
	for _, table := range tables {
		out[table] = e.dump(table)
	}
	return out
}

func (e *env) dump(table string) []string {
	e.t.Helper()
	rows, err := e.db.Reader().Query("SELECT * FROM " + table)
	if err != nil {
		e.t.Fatalf("dump %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		e.t.Fatalf("columns %s: %v", table, err)
	}
	var lines []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			e.t.Fatalf("scan %s: %v", table, err)
		}
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = fmt.Sprintf("%s=%v", columns[i], formatValue(v))
		}
		lines = append(lines, strings.Join(parts, "|"))
	}
	sort.Strings(lines)
	return lines
}

func formatValue(v any) any {
	if b, ok := v.([]byte); ok {
		return hex.EncodeToString(b)
	}
	return v
}

func assertSnapshotsEqual(t *testing.T, want, got map[string][]string) {
	t.Helper()
	for table, wantLines := range want {
		gotLines := got[table]
		if strings.Join(wantLines, "\n") != strings.Join(gotLines, "\n") {
			t.Errorf("table %s differs\nwant:\n%s\ngot:\n%s", table, strings.Join(wantLines, "\n"), strings.Join(gotLines, "\n"))
		}
	}
}

func putOp(n int, key string, bodyMsgID int64, size int64) wire.Op {
	if bodyMsgID == 0 {
		size = 0
	}
	op := wire.Op{
		Type: wire.OpPut, JobID: jobID(n), Key: key, BodyMsgID: bodyMsgID, StoredSize: size, PlaintextSize: size,
		ETag: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), ContentType: "text/plain",
		Writer: "key.tester", Timestamp: 1700000000 + int64(n),
	}
	if bodyMsgID == 0 {
		op.ETag = wire.EmptyMD5
		op.SHA256 = wire.EmptySHA256
	}
	return op
}

func delOp(n int, key string) wire.Op {
	return wire.Op{Type: wire.OpDel, JobID: jobID(n), Key: key, Writer: "key.tester", Timestamp: 1700000000 + int64(n)}
}

func bodyOp(n int, size int64) wire.Op {
	return wire.Op{Type: wire.OpBody, JobID: jobID(n), StoredSize: size}
}

func bucketCreateOp(n int, name string, channelID int64) wire.Op {
	return wire.Op{
		Type: wire.OpBucketCreate, JobID: jobID(n), Bucket: name, BucketChannelID: channelID,
		Policy: "private", DefaultEncryption: "none", Writer: "key.tester", Timestamp: 1700000000,
	}
}

func encfgOp(n int, bucketChannelID int64) wire.Op {
	return wire.Op{
		Type: wire.OpEncConfig, JobID: jobID(n), BucketChannelID: bucketChannelID, KeyID: strings.Repeat("c", 16),
		WrappedKey: make72(), KeyCheck: make61(), KEKFingerprint: "bkf1_" + strings.Repeat("d", 32),
		ConfigVersion: 1, Writer: "key.tester", Timestamp: 1700000000,
	}
}

func make72() []byte {
	return []byte(strings.Repeat("w", 72))
}

func make61() []byte {
	return []byte(strings.Repeat("k", 61))
}

var _ projection.Registry = (*fakeRegistry)(nil)
