package phase03

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	registryChannel int64 = 1000
	channelA        int64 = 2000
	channelB        int64 = 3000
	baseDate        int64 = 1763000000
	deploymentID          = "abcdefghijklmnopqrstuvwxyz"
)

var derivedTables = []string{
	"channels", "registry_markers", "buckets", "objects", "object_tombstones", "object_bodies",
	"encryption_keys", "projection_operations", "replay_log_rejects",
	"pending_message_cleanup", "replay_log", "replay_log_tamper", "deleted_messages",
}

type seqReader struct {
	seed    string
	counter uint64
	buf     []byte
}

func newSeqReader(seed string) *seqReader {
	return &seqReader{seed: seed}
}

func (r *seqReader) Read(p []byte) (int, error) {
	for len(r.buf) < len(p) {
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], r.counter)
		r.counter++
		sum := sha256.Sum256(append([]byte(r.seed), counter[:]...))
		r.buf = append(r.buf, sum[:]...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

type harness struct {
	t    *testing.T
	db   *sqlitedb.DB
	proj *core.Projection
	ring *keyring.Ring
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(dir, "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	keyPath := filepath.Join(dir, "server.key")
	if _, err := keyring.Generate(keyPath, newSeqReader("server key")); err != nil {
		t.Fatalf("generate key file: %v", err)
	}
	ring, err := keyring.LoadWithRand(keyPath, newSeqReader("ring randomness"))
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	t.Cleanup(ring.Close)
	proj, err := core.NewProjection(core.ProjectionDependencies{Logger: logger})
	if err != nil {
		t.Fatalf("new projection: %v", err)
	}
	h := &harness{t: t, db: db, proj: proj, ring: ring}
	h.exec(`INSERT INTO channels (channel_id, kind) VALUES (?, 'registry')`, registryChannel)
	return h
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	err := h.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		h.t.Fatalf("exec %q: %v", query, err)
	}
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		h.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func header(t *testing.T, op wire.Op) string {
	t.Helper()
	raw, err := wire.Format(op)
	if err != nil {
		t.Fatalf("format %s: %v", op.Type, err)
	}
	return raw
}

func observed(channelID, msgID int64, raw string, document bool) projection.Observed {
	return projection.Observed{ChannelID: channelID, MsgID: msgID, MsgDate: baseDate + msgID, IsDocument: document, RawText: raw}
}

func (h *harness) projectRaw(channelID, msgID int64, raw string, document bool) projection.Outcome {
	h.t.Helper()
	outcome, err := h.proj.Projector.ProjectFromOp(context.Background(), h.db, observed(channelID, msgID, raw, document))
	if err != nil {
		h.t.Fatalf("project msg %d of channel %d: %v", msgID, channelID, err)
	}
	return outcome
}

func (h *harness) project(channelID, msgID int64, op wire.Op) projection.Outcome {
	h.t.Helper()
	return h.projectRaw(channelID, msgID, header(h.t, op), op.Type == wire.OpBody)
}

func (h *harness) projectWithRepair(channelID, msgID int64, op wire.Op) (projection.Outcome, bool) {
	h.t.Helper()
	ctx := context.Background()
	raw := header(h.t, op)
	var outcome projection.Outcome
	repaired := false
	err := h.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		outcome, err = h.proj.Projector.ProjectFromOpTx(ctx, tx, observed(channelID, msgID, raw, op.Type == wire.OpBody))
		if err != nil {
			return err
		}
		key, ok := wire.KeyOf(op)
		if !ok || (op.Type != wire.OpPut && op.Type != wire.OpDel) {
			return nil
		}
		needs, err := h.proj.Rebuilder.NeedsKeyRepair(ctx, tx, channelID, key, msgID)
		if err != nil || !needs {
			return err
		}
		repaired = true
		_, err = h.proj.Rebuilder.RepairKeyTx(ctx, tx, channelID, key)
		return err
	})
	if err != nil {
		h.t.Fatalf("project msg %d of channel %d with repair: %v", msgID, channelID, err)
	}
	return outcome, repaired
}

func (h *harness) snapshot() map[string][]string {
	h.t.Helper()
	out := make(map[string][]string, len(derivedTables))
	for _, table := range derivedTables {
		out[table] = h.dump(table)
	}
	return out
}

func (h *harness) dump(table string) []string {
	h.t.Helper()
	rows, err := h.db.Reader().Query("SELECT * FROM " + table)
	if err != nil {
		h.t.Fatalf("dump %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		h.t.Fatalf("columns of %s: %v", table, err)
	}
	var lines []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			h.t.Fatalf("scan %s: %v", table, err)
		}
		parts := make([]string, len(values))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				v = hex.EncodeToString(b)
			}
			parts[i] = fmt.Sprintf("%s=%v", columns[i], v)
		}
		lines = append(lines, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("rows of %s: %v", table, err)
	}
	sort.Strings(lines)
	return lines
}

func assertSnapshotsEqual(t *testing.T, want, got map[string][]string) {
	t.Helper()
	for _, table := range derivedTables {
		wantText, gotText := strings.Join(want[table], "\n"), strings.Join(got[table], "\n")
		if wantText != gotText {
			t.Errorf("table %s differs\nwant:\n%s\ngot:\n%s", table, wantText, gotText)
		}
	}
}

func jobID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n)
}

func markerOp(n int, fingerprint string) wire.Op {
	return wire.Op{
		Type: wire.OpRegistry, DeploymentID: deploymentID, FormatVersion: 1, MinReaderVersion: 1,
		KEKFingerprint: fingerprint, PrimaryUserID: 4242, Writer: "key.tester", Timestamp: baseDate + int64(n),
	}
}

func bucketCreateOp(n int, name string, channelID int64) wire.Op {
	return wire.Op{
		Type: wire.OpBucketCreate, JobID: jobID(n), Bucket: name, BucketChannelID: channelID,
		Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600, Writer: "key.tester", Timestamp: baseDate + int64(n),
	}
}

func bucketConfigOp(n int, name string, channelID int64, policy string) wire.Op {
	op := bucketCreateOp(n, name, channelID)
	op.Type = wire.OpBucketConfig
	op.Policy = policy
	op.CacheMaxAge = 60
	return op
}

func bodyOp(n int, size int64) wire.Op {
	return wire.Op{Type: wire.OpBody, JobID: jobID(n), StoredSize: size}
}

func putOp(n int, key string, bodyMsgID, size int64) wire.Op {
	op := wire.Op{
		Type: wire.OpPut, JobID: jobID(n), Key: key, BodyMsgID: bodyMsgID, StoredSize: size, PlaintextSize: size,
		ETag: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), ContentType: "text/plain",
		Writer: "key.tester", Timestamp: baseDate + int64(n),
	}
	if bodyMsgID == 0 {
		op.StoredSize, op.PlaintextSize = 0, 0
		op.ETag, op.SHA256 = wire.EmptyMD5, wire.EmptySHA256
	}
	return op
}

func delOp(n int, key string) wire.Op {
	return wire.Op{Type: wire.OpDel, JobID: jobID(n), Key: key, Writer: "key.tester", Timestamp: baseDate + int64(n)}
}

func (h *harness) encfgOp(n int, bucketChannelID int64) (wire.Op, string, *keyring.Key) {
	h.t.Helper()
	dek, err := keyring.GenerateKey(newSeqReader(fmt.Sprintf("dek %d", n)))
	if err != nil {
		h.t.Fatalf("generate dek: %v", err)
	}
	h.t.Cleanup(dek.Clear)
	kid, err := keyring.NewKeyID(newSeqReader(fmt.Sprintf("kid %d", n)))
	if err != nil {
		h.t.Fatalf("new key id: %v", err)
	}
	wrap, check, err := h.ring.WrapDEK(dek, bucketChannelID, kid)
	if err != nil {
		h.t.Fatalf("wrap dek: %v", err)
	}
	op := wire.Op{
		Type: wire.OpEncConfig, JobID: jobID(n), BucketChannelID: bucketChannelID, KeyID: kid,
		WrappedKey: wrap[:], KeyCheck: check[:], KEKFingerprint: h.ring.FingerprintString(),
		ConfigVersion: 1, Writer: "key.tester", Timestamp: baseDate + int64(n),
	}
	return op, kid, dek
}
