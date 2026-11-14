package rebuild

import (
	"context"
	"errors"
	"testing"
)

func (e *env) seedEncryptionReplay() string {
	e.t.Helper()
	e.seedBucket("bucket-a", bucketChannel, 1)
	header := headerOf(e.t, encfgOp(7, bucketChannel))
	e.project(registryChannel, 101, header, false)
	return header
}

func (e *env) keyRows() int {
	e.t.Helper()
	return e.count(`SELECT COUNT(*) FROM encryption_keys`)
}

func (e *env) rebuildEncryption() (bool, error) {
	return e.rebuild.RebuildEncryptionConfig(context.Background(), e.db, registryChannel)
}

func TestRebuildEncryptionConfigFromReplayRestoresCanonicalConfig(t *testing.T) {
	e := newEnv(t)
	e.seedEncryptionReplay()
	want := e.snapshot("encryption_keys", "projection_operations", "replay_log_rejects")
	e.exec(`DELETE FROM encryption_keys`)

	found, err := e.rebuildEncryption()
	if err != nil || !found {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v)", found, err)
	}
	assertSnapshotsEqual(t, want, e.snapshot("encryption_keys", "projection_operations", "replay_log_rejects"))
}

func TestRebuildEncryptionConfigFromReplayLeavesPlaintextPolicyAbsent(t *testing.T) {
	e := newEnv(t)

	found, err := e.rebuildEncryption()
	if err != nil || found {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false, nil", found, err)
	}
	if e.keyRows() != 0 {
		t.Fatal("rebuild invented an encryption key")
	}
}

func TestRebuildEncryptionConfigFromReplayPreservesNoncanonicalDerivedConfig(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	e.project(registryChannel, 8, headerOf(t, encfgOp(8, bucketChannel)), false)
	e.exec(`DELETE FROM replay_log WHERE op_type = 'encfg'`)

	found, err := e.rebuildEncryption()
	if err != nil || found {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false, nil", found, err)
	}
	if e.keyRows() != 1 {
		t.Fatal("derived key without canonical replay was erased")
	}
}

func TestRebuildEncryptionConfigFromReplayRejectsTamper(t *testing.T) {
	e := newEnv(t)
	header := e.seedEncryptionReplay()
	e.exec(`INSERT INTO replay_log_tamper (channel_id, msg_id, old_hash, new_hash, new_raw_header) VALUES (?, 101, ?, ?, ?)`,
		registryChannel, shaOf(header), shaOf(header+"-edited"), header+"-edited")

	found, err := e.rebuildEncryption()
	if found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false and replay-invalid", found, err)
	}
	if e.keyRows() != 1 {
		t.Fatal("tampered replay changed derived keys")
	}
}

func TestRebuildEncryptionConfigFromReplayRejectsInvalidCanonicalHeader(t *testing.T) {
	e := newEnv(t)
	e.seedEncryptionReplay()
	e.exec(`UPDATE replay_log SET raw_header = 'TDX1|t=encfg|broken' WHERE channel_id = ? AND msg_id = 101`, registryChannel)

	found, err := e.rebuildEncryption()
	if found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false and replay-invalid", found, err)
	}
	if e.keyRows() != 1 {
		t.Fatal("corrupt replay changed derived keys")
	}
}

func TestRebuildEncryptionConfigFromReplayRejectsHashValidMalformedHeader(t *testing.T) {
	e := newEnv(t)
	e.seedEncryptionReplay()
	broken := "TDX1|t=encfg|broken"
	e.exec(`UPDATE replay_log SET raw_header = ?, first_seen_hash = ? WHERE channel_id = ? AND msg_id = 101`, broken, shaOf(broken), registryChannel)

	found, err := e.rebuildEncryption()
	if found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false and replay-invalid", found, err)
	}
}

func TestRebuildEncryptionConfigFromReplayRejectsWrongOpType(t *testing.T) {
	e := newEnv(t)
	e.seedBucket("bucket-a", bucketChannel, 1)
	header := headerOf(t, putOp(5, "a", 0, 0))
	e.appendReplay(registryChannel, 101, header, false)
	e.exec(`UPDATE replay_log SET op_type = 'encfg' WHERE channel_id = ? AND msg_id = 101`, registryChannel)

	found, err := e.rebuildEncryption()
	if found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("RebuildEncryptionConfig() = (%t, %v), want false and replay-invalid", found, err)
	}
}

func TestRebuildEncryptionConfigFromReplayRejectsInvalidBoundariesAndStorage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if found, err := e.rebuild.RebuildEncryptionConfig(ctx, nil, registryChannel); found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("nil writer = (%t, %v)", found, err)
	}
	if found, err := e.rebuild.RebuildEncryptionConfig(ctx, e.db, 0); found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("zero channel = (%t, %v)", found, err)
	}
	e.exec(`DROP TABLE replay_log`)
	if found, err := e.rebuildEncryption(); found || !errors.Is(err, ErrEncryptionConfigReplayInvalid) {
		t.Fatalf("missing replay storage = (%t, %v)", found, err)
	}
}
