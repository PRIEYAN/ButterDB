package phase03

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type message struct {
	channelID int64
	msgID     int64
	raw       string
	document  bool
	want      projection.OutcomeKind
}

func TestDuplicateProjectionIsIdempotent(t *testing.T) {
	h := newHarness(t)
	encfg, _, _ := h.encfgOp(5, channelA)
	messages := []message{
		{registryChannel, 1, header(t, markerOp(1, h.ring.FingerprintString())), false, projection.OutcomeApplied},
		{registryChannel, 2, header(t, bucketCreateOp(2, "bucket-a", channelA)), false, projection.OutcomeApplied},
		{registryChannel, 3, header(t, bucketConfigOp(3, "bucket-a", channelA, "public-read")), false, projection.OutcomeApplied},
		{registryChannel, 4, "TDX1|t=future|j=" + jobID(4), false, projection.OutcomeRejected},
		{registryChannel, 5, header(t, encfg), false, projection.OutcomeApplied},
		{registryChannel, 6, "TDX1|not a header", false, projection.OutcomeRejected},
		{channelA, 10, header(t, bodyOp(10, 64)), true, projection.OutcomeApplied},
		{channelA, 11, header(t, putOp(11, "docs/a.txt", 10, 64)), false, projection.OutcomeApplied},
		{channelA, 12, header(t, putOp(12, "empty.txt", 0, 0)), false, projection.OutcomeApplied},
		{channelA, 13, header(t, delOp(13, "empty.txt")), false, projection.OutcomeApplied},
		{channelA, 14, header(t, putOp(11, "docs/a.txt", 10, 64)), false, projection.OutcomeRejected},
		{channelA, 15, "plain chat, not a control message", false, projection.OutcomeIgnored},
	}
	for _, m := range messages {
		got := h.projectRaw(m.channelID, m.msgID, m.raw, m.document)
		if got.Kind != m.want {
			t.Fatalf("first projection of msg %d in channel %d: kind %q, want %q", m.msgID, m.channelID, got.Kind, m.want)
		}
	}
	before := h.snapshot()
	if len(before["objects"]) != 1 || len(before["object_tombstones"]) != 1 || len(before["encryption_keys"]) != 1 {
		t.Fatalf("scenario did not project the expected state: %d objects, %d tombstones, %d keys",
			len(before["objects"]), len(before["object_tombstones"]), len(before["encryption_keys"]))
	}
	if h.proj.Generations.Generation(channelA) != 1 {
		t.Fatalf("key generation = %d, want 1", h.proj.Generations.Generation(channelA))
	}

	for round := 1; round <= 2; round++ {
		for _, m := range messages {
			got := h.projectRaw(m.channelID, m.msgID, m.raw, m.document)
			want := projection.OutcomeAlreadySeen
			if m.want == projection.OutcomeIgnored {
				want = projection.OutcomeIgnored
			}
			if m.want == projection.OutcomeRejected && m.msgID == 6 {
				want = projection.OutcomeRejected
			}
			if got.Kind != want {
				t.Errorf("round %d: msg %d in channel %d: kind %q, want %q", round, m.msgID, m.channelID, got.Kind, want)
			}
		}
		assertSnapshotsEqual(t, before, h.snapshot())
	}
	if h.proj.Generations.Generation(channelA) != 1 {
		t.Fatalf("key generation after replays = %d, want 1", h.proj.Generations.Generation(channelA))
	}
	if got := h.count(`SELECT COUNT(*) FROM replay_log_tamper`); got != 0 {
		t.Fatalf("replaying identical headers recorded %d tamper rows", got)
	}
}
