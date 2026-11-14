package apply

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	registryChannel = int64(1000)
	bucketChannel   = int64(2000)
	otherChannel    = int64(3000)
	fingerprintA    = "bkf1_00000000000000000000000000000001"
	fingerprintB    = "bkf1_00000000000000000000000000000002"
	keyIDA          = "0123456789abcdef"
)

type generationLog struct {
	mu       sync.Mutex
	advanced []int64
}

func (g *generationLog) Advance(bucketChannelID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advanced = append(g.advanced, bucketChannelID)
}

func (g *generationLog) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.advanced)
}

type env struct {
	t        *testing.T
	db       *sqlitedb.DB
	registry *Registry
	gen      *generationLog
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(ctx, sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	gen := &generationLog{}
	registry, err := NewRegistry(Dependencies{Generation: gen})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	e := &env{t: t, db: db, registry: registry, gen: gen}
	e.exec(`INSERT INTO channels (channel_id, kind) VALUES (?1, 'registry')`, registryChannel)
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	err := e.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) apply(obs projection.Observed, op wire.Op) (projection.ApplyResult, error) {
	e.t.Helper()
	applier, ok := e.registry.Lookup(op.Type)
	if !ok {
		e.t.Fatalf("no applier for %q", op.Type)
	}
	var result projection.ApplyResult
	var applyErr error
	err := e.db.Write(context.Background(), func(tx *sql.Tx) error {
		return e.applyInSavepoint(tx, applier, projection.ApplyInput{Observed: obs, Op: op}, &result, &applyErr)
	})
	if err != nil {
		e.t.Fatalf("write: %v", err)
	}
	return result, applyErr
}

func keepsWritesAfter(err error) bool {
	if err == nil {
		return true
	}
	var reject *projection.Reject
	return errors.As(err, &reject) && reject.Code == projection.RejectDuplicateOp
}

func (e *env) applyInSavepoint(tx *sql.Tx, applier projection.Applier, in projection.ApplyInput, result *projection.ApplyResult, applyErr *error) error {
	ctx := context.Background()
	if _, err := tx.ExecContext(ctx, `SAVEPOINT projection_log_apply`); err != nil {
		return err
	}
	*result, *applyErr = applier.Apply(ctx, tx, in)
	if !keepsWritesAfter(*applyErr) {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO projection_log_apply`); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `RELEASE projection_log_apply`)
	return err
}

func (e *env) mustApply(obs projection.Observed, op wire.Op, want projection.OutcomeKind) {
	e.t.Helper()
	result, err := e.apply(obs, op)
	if err != nil {
		e.t.Fatalf("apply %s at %d: %v", op.Type, obs.MsgID, err)
	}
	if result.Kind != want {
		e.t.Fatalf("apply %s at %d: kind %q, want %q", op.Type, obs.MsgID, result.Kind, want)
	}
}

func (e *env) mustReject(obs projection.Observed, op wire.Op, want projection.RejectCode) {
	e.t.Helper()
	_, err := e.apply(obs, op)
	reject, ok := err.(*projection.Reject)
	if !ok {
		e.t.Fatalf("apply %s at %d: error %v, want reject %q", op.Type, obs.MsgID, err, want)
	}
	if reject.Code != want {
		e.t.Fatalf("apply %s at %d: reject %q, want %q", op.Type, obs.MsgID, reject.Code, want)
	}
}

func (e *env) createBucket(channelID int64, name string, msgID int64) {
	e.t.Helper()
	e.mustApply(registryObs(msgID), bucketOp(wire.OpBucketCreate, name, channelID, jobID(int(msgID))), projection.OutcomeApplied)
}

func jobID(n int) string {
	suffix := strings.Repeat("0", 12-len(itoa(n))) + itoa(n)
	return "00000000-0000-4000-8000-" + suffix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func registryObs(msgID int64) projection.Observed {
	return projection.Observed{ChannelID: registryChannel, MsgID: msgID, MsgDate: 1700000000 + msgID}
}

func bucketObs(channelID, msgID int64) projection.Observed {
	return projection.Observed{ChannelID: channelID, MsgID: msgID, MsgDate: 1700000000 + msgID}
}

func bodyObs(channelID, msgID int64) projection.Observed {
	obs := bucketObs(channelID, msgID)
	obs.IsDocument = true
	return obs
}

func bucketOp(t wire.OpType, name string, channelID int64, job string) wire.Op {
	return wire.Op{
		Type: t, JobID: job, Bucket: name, BucketChannelID: channelID,
		Policy: "private", DefaultEncryption: "none", CacheMaxAge: 3600, Timestamp: 1700000000,
	}
}

func putOp(key string, bodyMsgID int64, job string) wire.Op {
	op := wire.Op{
		Type: wire.OpPut, JobID: job, Key: key, BodyMsgID: bodyMsgID, StoredSize: 100, PlaintextSize: 100,
		ETag: "d41d8cd98f00b204e9800998ecf8427e", SHA256: wire.EmptySHA256, ContentType: "text/plain",
		Writer: "key.test", Timestamp: 1700000000,
	}
	return op
}

func delOp(key string, job string) wire.Op {
	return wire.Op{Type: wire.OpDel, JobID: job, Key: key, Writer: "key.test", Timestamp: 1700000000}
}

func encfgOp(bucketChannelID int64, keyID string, version int, fingerprint string, job string) wire.Op {
	return wire.Op{
		Type: wire.OpEncConfig, JobID: job, BucketChannelID: bucketChannelID, KeyID: keyID,
		WrappedKey: make([]byte, 72), KeyCheck: make([]byte, 61), KEKFingerprint: fingerprint,
		ConfigVersion: version, Timestamp: 1700000000,
	}
}

func markerOp(deployment string, fv, mr int, fingerprint string, uid int64, writer string) wire.Op {
	return wire.Op{
		Type: wire.OpRegistry, DeploymentID: deployment, FormatVersion: fv, MinReaderVersion: mr,
		KEKFingerprint: fingerprint, PrimaryUserID: uid, Writer: writer, Timestamp: 1700000000,
	}
}
