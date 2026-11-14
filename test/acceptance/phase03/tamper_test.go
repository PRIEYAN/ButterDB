package phase03

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
)

func TestTamperedReplayIsFatal(t *testing.T) {
	t.Run("an edited header records tamper and keeps the first op canonical", func(t *testing.T) {
		h := newHarness(t)
		h.project(registryChannel, 1, markerOp(1, h.ring.FingerprintString()))
		h.project(registryChannel, 2, bucketCreateOp(2, "bucket-a", channelA))
		h.project(channelA, 10, bodyOp(10, 64))
		first := header(t, putOp(11, "docs/a.txt", 10, 64))
		if got := h.projectRaw(channelA, 11, first, false); got.Kind != projection.OutcomeApplied {
			t.Fatalf("first put: kind %q, want applied", got.Kind)
		}
		before := h.snapshot()

		edited := putOp(11, "docs/other.txt", 10, 64)
		edited.StoredSize, edited.PlaintextSize = 65, 65
		got := h.projectRaw(channelA, 11, header(t, edited), false)
		if got.Kind != projection.OutcomeTampered {
			t.Fatalf("edited put: kind %q, want tampered", got.Kind)
		}
		if got := h.count(`SELECT COUNT(*) FROM replay_log_tamper WHERE channel_id = ? AND msg_id = 11`, channelA); got != 1 {
			t.Fatalf("tamper rows = %d, want 1", got)
		}
		if got := h.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ? AND object_key = 'docs/other.txt'`, channelA); got != 0 {
			t.Fatalf("edited key was projected: %d rows", got)
		}
		if got := h.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ? AND object_key = 'docs/a.txt' AND stored_size = 64`, channelA); got != 1 {
			t.Fatalf("first op is no longer canonical: %d rows", got)
		}
		var canonical string
		err := h.db.Reader().QueryRow(`SELECT raw_header FROM replay_log WHERE channel_id = ? AND msg_id = 11`, channelA).Scan(&canonical)
		if err != nil || canonical != first {
			t.Fatalf("replay_log header = %q (%v), want the first header", canonical, err)
		}

		h.projectRaw(channelA, 11, header(t, edited), false)
		if got := h.count(`SELECT observations FROM replay_log_tamper WHERE channel_id = ? AND msg_id = 11`, channelA); got != 2 {
			t.Fatalf("tamper observations = %d, want 2", got)
		}

		after := h.snapshot()
		delete(after, "replay_log_tamper")
		delete(before, "replay_log_tamper")
		assertSnapshotsEqual(t, before, after)
	})

	t.Run("a tampered canonical encfg makes the encryption rebuild fail", func(t *testing.T) {
		tests := []struct {
			name   string
			tamper func(h *harness, encfgHeader string, edited string)
		}{
			{
				name: "edited header observed again",
				tamper: func(h *harness, _ string, edited string) {
					if got := h.projectRaw(registryChannel, 3, edited, false); got.Kind != projection.OutcomeTampered {
						t.Fatalf("edited encfg: kind %q, want tampered", got.Kind)
					}
				},
			},
			{
				name: "canonical header overwritten in the replay log",
				tamper: func(h *harness, _ string, edited string) {
					h.exec(`UPDATE replay_log SET raw_header = ? WHERE channel_id = ? AND msg_id = 3`, edited, registryChannel)
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := newHarness(t)
				h.project(registryChannel, 1, markerOp(1, h.ring.FingerprintString()))
				h.project(registryChannel, 2, bucketCreateOp(2, "bucket-a", channelA))
				encfg, _, _ := h.encfgOp(3, channelA)
				good := header(t, encfg)
				if got := h.projectRaw(registryChannel, 3, good, false); got.Kind != projection.OutcomeApplied {
					t.Fatalf("encfg: kind %q, want applied", got.Kind)
				}
				found, err := h.proj.Rebuilder.RebuildEncryptionConfig(context.Background(), h.db, registryChannel)
				if err != nil || !found {
					t.Fatalf("rebuild of an untampered encfg = %v, %v; want true, nil", found, err)
				}
				keys := h.snapshot()["encryption_keys"]

				other, _, _ := h.encfgOp(3, channelA)
				other.KeyID = "ffffffffffffffff"
				tt.tamper(h, good, header(t, other))

				found, err = h.proj.Rebuilder.RebuildEncryptionConfig(context.Background(), h.db, registryChannel)
				if found || !errors.Is(err, rebuild.ErrEncryptionConfigReplayInvalid) {
					t.Fatalf("rebuild of a tampered encfg = %v, %v; want false, ErrEncryptionConfigReplayInvalid", found, err)
				}
				assertSnapshotsEqual(t, map[string][]string{"encryption_keys": keys}, map[string][]string{"encryption_keys": h.snapshot()["encryption_keys"]})
			})
		}
	})
}
