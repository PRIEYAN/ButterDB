package apply

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestBucketCreateConfigureDelete(t *testing.T) {
	e := newEnv(t)
	create := bucketOp(wire.OpBucketCreate, "photos", bucketChannel, jobID(2))
	e.mustApply(registryObs(2), create, projection.OutcomeApplied)
	if n := e.count(`SELECT COUNT(*) FROM buckets WHERE channel_id = ?1 AND name = 'photos' AND policy = 'private' AND encryption_default = 'none' AND cache_max_age_seconds = 3600 AND created_msg_id = 2 AND config_msg_id = 2 AND created_date = 1700000002 AND deleted_msg_id = 0`, bucketChannel); n != 1 {
		t.Fatalf("bucket row not stored as expected")
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE channel_id = ?1 AND kind = 'bucket' AND state = 'active'`, bucketChannel); n != 1 {
		t.Fatalf("bucket channel row missing")
	}
	e.mustApply(registryObs(2), create, projection.OutcomeApplied)

	cfg := bucketOp(wire.OpBucketConfig, "photos", bucketChannel, jobID(5))
	cfg.Policy, cfg.DefaultEncryption, cfg.CacheMaxAge = "public-read", "tde1", 60
	e.mustApply(registryObs(5), cfg, projection.OutcomeApplied)
	older := bucketOp(wire.OpBucketConfig, "photos", bucketChannel, jobID(4))
	e.mustApply(registryObs(4), older, projection.OutcomeSuperseded)
	if n := e.count(`SELECT COUNT(*) FROM buckets WHERE policy = 'public-read' AND encryption_default = 'tde1' AND cache_max_age_seconds = 60 AND config_msg_id = 5`); n != 1 {
		t.Fatalf("configuration not last writer wins")
	}

	e.mustApply(bucketObs(bucketChannel, 10), putOp("k", 9, jobID(10)), projection.OutcomeApplied)
	e.mustApply(bodyObs(bucketChannel, 9), wire.Op{Type: wire.OpBody, JobID: jobID(9), StoredSize: 100}, projection.OutcomeApplied)
	e.exec(`INSERT INTO encryption_keys (bucket_channel_id, key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id, job_id, msg_date)
VALUES (?1, ?2, zeroblob(72), zeroblob(61), ?3, 1, 3, 3, ?4, 0)`, bucketChannel, keyIDA, fingerprintA, jobID(3))

	before := e.gen.count()
	remove := bucketOp(wire.OpBucketDelete, "photos", bucketChannel, jobID(20))
	e.mustApply(registryObs(20), remove, projection.OutcomeApplied)
	for _, table := range []string{"objects", "object_tombstones", "object_bodies", "pending_message_cleanup"} {
		if n := e.count(`SELECT COUNT(*) FROM `+table+` WHERE channel_id = ?1`, bucketChannel); n != 0 {
			t.Fatalf("%s kept %d rows", table, n)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM encryption_keys WHERE bucket_channel_id = ?1`, bucketChannel); n != 0 {
		t.Fatalf("encryption keys kept %d rows", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM buckets WHERE channel_id = ?1 AND deleted_msg_id = 20 AND object_count = 0 AND stored_bytes = 0`, bucketChannel); n != 1 {
		t.Fatalf("bucket not marked deleted")
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE channel_id = ?1 AND state = 'deleted'`, bucketChannel); n != 1 {
		t.Fatalf("channel not marked deleted")
	}
	if n := e.count(`SELECT COUNT(*) FROM pending_channel_cleanup WHERE channel_id = ?1 AND bdel_msg_id = 20`, bucketChannel); n != 1 {
		t.Fatalf("channel cleanup not queued")
	}
	if e.gen.count() != before+1 {
		t.Fatalf("generation advanced %d times, want once", e.gen.count()-before)
	}
	e.mustApply(registryObs(20), remove, projection.OutcomeApplied)
	e.mustReject(bucketObs(bucketChannel, 30), putOp("k", 29, jobID(30)), projection.RejectBucketNotFound)
	e.mustReject(registryObs(31), cfg, projection.RejectBucketNotFound)

	e.mustApply(registryObs(40), bucketOp(wire.OpBucketCreate, "photos", otherChannel, jobID(40)), projection.OutcomeApplied)
}

func TestBucketCreateRejects(t *testing.T) {
	cases := []struct {
		name string
		obs  projection.Observed
		op   wire.Op
		want projection.RejectCode
	}{
		{"invalid name", registryObs(5), bucketOp(wire.OpBucketCreate, "UPPER..case", otherChannel, jobID(5)), projection.RejectBucketNameInvalid},
		{"name in use", registryObs(5), bucketOp(wire.OpBucketCreate, "photos", otherChannel, jobID(5)), projection.RejectBucketExists},
		{"channel in use", registryObs(5), bucketOp(wire.OpBucketCreate, "videos", bucketChannel, jobID(5)), projection.RejectBucketExists},
		{"registry channel reused", registryObs(5), bucketOp(wire.OpBucketCreate, "videos", registryChannel, jobID(5)), projection.RejectBucketExists},
		{"in a bucket channel", bucketObs(bucketChannel, 5), bucketOp(wire.OpBucketCreate, "videos", otherChannel, jobID(5)), projection.RejectWrongChannel},
		{"on a document", func() projection.Observed { o := registryObs(5); o.IsDocument = true; return o }(), bucketOp(wire.OpBucketCreate, "videos", otherChannel, jobID(5)), projection.RejectWrongCarrier},
		{"same job at another message", registryObs(5), bucketOp(wire.OpBucketCreate, "videos", otherChannel, jobID(2)), projection.RejectDuplicateOp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBucketEnv(t)
			e.exec(`INSERT INTO projection_operations (channel_id, op_id, op_type, msg_id, outcome) VALUES (?1, ?2, 'bcreate', 2, 'applied')`, registryChannel, jobID(2))
			e.mustReject(tc.obs, tc.op, tc.want)
			if n := e.count(`SELECT COUNT(*) FROM buckets`); n != 1 {
				t.Fatalf("bucket rows %d, want 1", n)
			}
		})
	}
}

func TestBucketConfigAndDeleteNeedMatchingName(t *testing.T) {
	e := newBucketEnv(t)
	e.mustReject(registryObs(5), bucketOp(wire.OpBucketConfig, "other-name", bucketChannel, jobID(5)), projection.RejectBucketNotFound)
	e.mustReject(registryObs(6), bucketOp(wire.OpBucketDelete, "other-name", bucketChannel, jobID(6)), projection.RejectBucketNotFound)
	e.mustReject(registryObs(7), bucketOp(wire.OpBucketConfig, "photos", otherChannel, jobID(7)), projection.RejectBucketNotFound)
}
