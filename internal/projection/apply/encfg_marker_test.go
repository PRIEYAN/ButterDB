package apply

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const deployment = "6pzyoxbfwcf6ubwcq5gpvjg5c4"

func (e *env) keyRow(keyID string) (version int, firstMsg, msg int64, fingerprint string, found bool) {
	e.t.Helper()
	err := e.db.Reader().QueryRow(
		`SELECT config_version, first_msg_id, msg_id, kek_fingerprint FROM encryption_keys WHERE bucket_channel_id = ?1 AND key_id = ?2`,
		bucketChannel, keyID).Scan(&version, &firstMsg, &msg, &fingerprint)
	return version, firstMsg, msg, fingerprint, err == nil
}

func TestEncfgStoresWrappedKey(t *testing.T) {
	e := newBucketEnv(t)
	e.mustApply(registryObs(1), markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup"), projection.OutcomeApplied)
	op := encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(4))
	op.WrappedKey[0], op.KeyCheck[0] = 7, 9
	before := e.gen.count()
	e.mustApply(registryObs(4), op, projection.OutcomeApplied)
	version, first, msg, fingerprint, found := e.keyRow(keyIDA)
	if !found || version != 1 || first != 4 || msg != 4 || fingerprint != fingerprintA {
		t.Fatalf("key row %v %d %d %d %q", found, version, first, msg, fingerprint)
	}
	if n := e.count(`SELECT COUNT(*) FROM encryption_keys WHERE length(wrapped_dek) = 72 AND hex(substr(wrapped_dek, 1, 1)) = '07' AND length(key_check) = 61 AND hex(substr(key_check, 1, 1)) = '09'`); n != 1 {
		t.Fatalf("wrapped key bytes not stored verbatim")
	}
	if e.gen.count() != before+1 {
		t.Fatalf("generation advanced %d times, want once", e.gen.count()-before)
	}
	e.mustApply(registryObs(4), op, projection.OutcomeApplied)

	rewrap := encfgOp(bucketChannel, keyIDA, 2, fingerprintA, jobID(6))
	e.mustApply(registryObs(6), rewrap, projection.OutcomeApplied)
	version, first, msg, _, _ = e.keyRow(keyIDA)
	if version != 2 || first != 4 || msg != 6 {
		t.Fatalf("re-wrap row version %d first %d msg %d", version, first, msg)
	}
	e.mustReject(registryObs(8), encfgOp(bucketChannel, keyIDA, 2, fingerprintA, jobID(8)), projection.RejectDuplicateOp)
	e.mustReject(registryObs(9), encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(9)), projection.RejectDuplicateOp)

	e.mustApply(registryObs(10), encfgOp(bucketChannel, "fedcba9876543210", 1, fingerprintA, jobID(10)), projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM encryption_keys WHERE bucket_channel_id = ?1`, bucketChannel); n != 2 {
		t.Fatalf("keys %d, want 2", n)
	}
}

func TestEncfgRejects(t *testing.T) {
	cases := []struct {
		name string
		obs  projection.Observed
		op   wire.Op
		want projection.RejectCode
	}{
		{"unknown bucket", registryObs(4), encfgOp(otherChannel, keyIDA, 1, fingerprintA, jobID(4)), projection.RejectBucketNotFound},
		{"fingerprint differs from marker", registryObs(4), encfgOp(bucketChannel, keyIDA, 1, fingerprintB, jobID(4)), projection.RejectKEKMismatch},
		{"in a bucket channel", bucketObs(bucketChannel, 4), encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(4)), projection.RejectWrongChannel},
		{"on a document", func() projection.Observed { o := registryObs(4); o.IsDocument = true; return o }(), encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(4)), projection.RejectWrongCarrier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			e.mustApply(registryObs(1), markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup"), projection.OutcomeApplied)
			e.mustReject(tc.obs, tc.op, tc.want)
			if n := e.count(`SELECT COUNT(*) FROM encryption_keys`); n != 0 {
				t.Fatalf("key rows %d, want 0", n)
			}
		})
	}
}

func TestEncfgAfterRekeyMarkerUsesMarkerInEffect(t *testing.T) {
	e := newBucketEnv(t)
	e.mustApply(registryObs(1), markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup"), projection.OutcomeApplied)
	e.mustApply(registryObs(4), encfgOp(bucketChannel, keyIDA, 1, fingerprintA, jobID(4)), projection.OutcomeApplied)
	e.mustApply(registryObs(5), markerOp(deployment, 1, 1, fingerprintB, 777, "sys.rekey"), projection.OutcomeApplied)
	e.mustApply(registryObs(6), encfgOp(bucketChannel, keyIDA, 2, fingerprintB, jobID(6)), projection.OutcomeApplied)
	if _, _, _, fingerprint, _ := e.keyRow(keyIDA); fingerprint != fingerprintB {
		t.Fatalf("fingerprint %q, want %q", fingerprint, fingerprintB)
	}
}

func TestRegistryMarkerRecorded(t *testing.T) {
	e := newEnv(t)
	first := markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup")
	e.mustApply(registryObs(2), first, projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM registry_markers WHERE channel_id = ?1 AND msg_id = 2 AND deployment_id = ?2 AND format_version = 1 AND min_reader = 1 AND kek_fingerprint = ?3 AND primary_user_id = 777 AND writer = 'sys.setup' AND msg_date = 1700000002`, registryChannel, deployment, fingerprintA); n != 1 {
		t.Fatalf("marker row not stored as expected")
	}
	e.mustApply(registryObs(2), first, projection.OutcomeApplied)

	cases := []struct {
		name string
		msg  int64
		op   wire.Op
		want projection.RejectCode
		ok   bool
	}{
		{"other deployment", 3, markerOp("aaaaaaaaaaaaaaaaaaaaaaaaaa", 1, 1, fingerprintA, 777, ""), projection.RejectMarkerConflict, false},
		{"other user", 3, markerOp(deployment, 1, 1, fingerprintA, 778, ""), projection.RejectMarkerConflict, false},
		{"lower format version", 3, markerOp(deployment, 0, 0, fingerprintA, 777, ""), projection.RejectMarkerConflict, false},
		{"new fingerprint without rekey", 3, markerOp(deployment, 1, 1, fingerprintB, 777, "sys.upgrade"), projection.RejectMarkerConflict, false},
		{"upgrade raises versions", 4, markerOp(deployment, 2, 2, fingerprintA, 777, "sys.upgrade"), "", true},
		{"rekey changes fingerprint", 5, markerOp(deployment, 2, 2, fingerprintB, 777, "sys.rekey"), "", true},
		{"lower mr after raise", 6, markerOp(deployment, 2, 1, fingerprintB, 777, ""), projection.RejectMarkerConflict, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ok {
				e.mustApply(registryObs(tc.msg), tc.op, projection.OutcomeApplied)
				return
			}
			e.mustReject(registryObs(tc.msg), tc.op, tc.want)
		})
	}
	if n := e.count(`SELECT COUNT(*) FROM registry_markers`); n != 3 {
		t.Fatalf("markers %d, want 3", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM registry_markers WHERE msg_id = 5 AND kek_fingerprint = ?1`, fingerprintB); n != 1 {
		t.Fatalf("rekey marker missing")
	}
}

func TestRegistryMarkerScope(t *testing.T) {
	e := newBucketEnv(t)
	marker := markerOp(deployment, 1, 1, fingerprintA, 777, "sys.setup")
	e.mustReject(bucketObs(bucketChannel, 5), marker, projection.RejectWrongChannel)
	doc := registryObs(5)
	doc.IsDocument = true
	e.mustReject(doc, marker, projection.RejectWrongCarrier)
	e.mustReject(bucketObs(otherChannel, 5), marker, projection.RejectWrongChannel)
}
