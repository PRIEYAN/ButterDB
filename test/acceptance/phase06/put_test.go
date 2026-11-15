package phase06

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

func TestPUTAcknowledgesOnlyCommittedTelegramState(t *testing.T) {
	t.Run("an acknowledged put is a projected t=put message in Telegram", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		body := []byte("hello butterdb")
		meta := w.mustPut("photos", "greeting.txt", body)

		controls := w.network.SentControls(info.ChannelID)
		var putMsgID int64
		for _, control := range controls {
			if strings.HasPrefix(control.Text, "TDX1|t=put|") {
				putMsgID = control.MsgID
			}
		}
		if putMsgID == 0 || meta.PutMsgID != putMsgID {
			t.Fatalf("returned put message id %d, Telegram holds put message %d", meta.PutMsgID, putMsgID)
		}
		files := w.network.SentFiles(info.ChannelID)
		if len(files) != 1 || files[0].MsgID != meta.BodyMsgID || files[0].Size != int64(len(body)) {
			t.Fatalf("sent files = %+v, want one body document %d of %d bytes", files, meta.BodyMsgID, len(body))
		}
		if !strings.HasPrefix(files[0].Caption, "TDX1|t=body|j="+meta.JobID) {
			t.Fatalf("body caption = %q", files[0].Caption)
		}
		if got := w.projected(`SELECT put_msg_id, body_msg_id, stored_size FROM objects WHERE channel_id = ?1 AND object_key = 'greeting.txt'`, info.ChannelID); len(got) != 1 || got[0] != itoa(putMsgID)+"|"+itoa(meta.BodyMsgID)+"|14" {
			t.Fatalf("projected object row = %v", got)
		}
		if got := w.controlRows(`SELECT state, put_msg_id FROM upload_jobs WHERE job_id = ?1`, meta.JobID); len(got) != 1 || got[0] != "committed|"+itoa(putMsgID) {
			t.Fatalf("job row = %v", got)
		}
		if got := w.documentBytes(info.ChannelID, meta.BodyMsgID, int64(len(body))); !bytes.Equal(got, body) {
			t.Fatalf("stored document = %q, want the uploaded bytes", got)
		}
		spooled, err := os.ReadDir(filepath.Join(w.dir, "spool"))
		if err != nil || len(spooled) != 0 {
			t.Fatalf("spool directory = %v, %v; want empty after commit", spooled, err)
		}
	})

	t.Run("an unknown put outcome is never acknowledged and commits nothing", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		unknown := faketg.Fault{Kind: faketg.FaultOutcomeUnknown}
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, unknown, unknown, unknown); err != nil {
			t.Fatalf("queue faults: %v", err)
		}
		_, err := w.put("photos", "greeting.txt", []byte("hello"))
		if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("put = %v, want ErrSendOutcomeUnknown", err)
		}
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("objects after an unacknowledged put = %d, want 0", got)
		}
		if got := w.controlRows(`SELECT state FROM upload_jobs`); len(got) != 1 || got[0] != "uncertain_put" {
			t.Fatalf("job states = %v, want one uncertain_put", got)
		}
	})

	t.Run("a put that Telegram accepted but whose reply failed is projected and acknowledged", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultAcceptedWithError}); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		meta, err := w.put("photos", "greeting.txt", []byte("hello"))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1 AND put_msg_id = ?2`, info.ChannelID, meta.PutMsgID); got != 1 {
			t.Fatalf("projected objects for put %d = %d, want 1", meta.PutMsgID, got)
		}
		puts := 0
		for _, control := range w.network.SentControls(info.ChannelID) {
			if strings.HasPrefix(control.Text, "TDX1|t=put|") {
				puts++
			}
		}
		if puts != 1 {
			t.Fatalf("put messages in Telegram = %d, want 1", puts)
		}
	})

	t.Run("an encrypted put stores ciphertext that decrypts to the upload", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("vault", "tde1")
		plain := bytes.Repeat([]byte("secret "), 5000)
		meta := w.mustPut("vault", "notes.txt", plain)
		if !meta.Encrypted || meta.PlaintextSize != int64(len(plain)) || meta.StoredSize <= meta.PlaintextSize {
			t.Fatalf("meta = %+v", meta)
		}
		stored := w.documentBytes(info.ChannelID, meta.BodyMsgID, meta.StoredSize)
		if bytes.Contains(stored, []byte("secret secret")) || !bytes.HasPrefix(stored, []byte("TDE1")) {
			t.Fatal("stored document is not TDE1 ciphertext")
		}
		keys, err := bucketrepo.New(w.db)
		if err != nil {
			t.Fatal(err)
		}
		key, found, err := keys.Key(context.Background(), info.ChannelID, meta.KeyID)
		if err != nil || !found {
			t.Fatalf("key lookup = %v, %v", found, err)
		}
		dek, err := w.ring.UnwrapDEK(key.WrappedDEK, key.KeyCheck, info.ChannelID, key.KeyID)
		if err != nil {
			t.Fatalf("unwrap: %v", err)
		}
		defer dek.Clear()
		var out bytes.Buffer
		if _, err := decryptStream(&out, bytes.NewReader(stored), dek.Bytes()); err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Fatal("decrypted bytes differ from the upload")
		}
	})
}

func TestUncertainSendIsReconciled(t *testing.T) {
	t.Run("a put message that Telegram holds is adopted by the sweeper without a second send", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		delivered := faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, delivered, delivered, delivered); err != nil {
			t.Fatalf("queue faults: %v", err)
		}
		if _, err := w.put("photos", "report.txt", []byte("quarterly")); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("put = %v, want ErrSendOutcomeUnknown", err)
		}
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("objects before reconciliation = %d, want 0", got)
		}
		w.clk.Advance(10 * time.Minute)
		w.write.Sweeper.RunOnce(context.Background())
		if got := w.projected(`SELECT put_msg_id FROM objects WHERE channel_id = ?1 AND object_key = 'report.txt'`, info.ChannelID); len(got) != 1 {
			t.Fatalf("objects after reconciliation = %v, want one", got)
		}
		if got := w.controlRows(`SELECT state FROM upload_jobs`); len(got) != 1 || got[0] != "committed" {
			t.Fatalf("job states = %v, want one committed", got)
		}
		if puts := countPutMessages(w, info.ChannelID); puts != 1 {
			t.Fatalf("put messages in Telegram = %d, want 1", puts)
		}
	})

	t.Run("a put message Telegram never stored is resent once with the same identity", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		lost := faketg.Fault{Kind: faketg.FaultOutcomeUnknown}
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, lost, lost, lost); err != nil {
			t.Fatalf("queue faults: %v", err)
		}
		if _, err := w.put("photos", "report.txt", []byte("quarterly")); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
			t.Fatalf("put = %v, want ErrSendOutcomeUnknown", err)
		}
		w.clk.Advance(10 * time.Minute)
		w.write.Sweeper.RunOnce(context.Background())
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1 AND object_key = 'report.txt'`, info.ChannelID); got != 1 {
			t.Fatalf("objects after reconciliation = %d, want 1", got)
		}
		if puts := countPutMessages(w, info.ChannelID); puts != 1 {
			t.Fatalf("put messages in Telegram = %d, want 1", puts)
		}
		if files := w.network.SentFiles(info.ChannelID); len(files) != 1 {
			t.Fatalf("body documents in Telegram = %d, want 1", len(files))
		}
		jobs := w.controlRows(`SELECT job_id FROM upload_jobs`)
		if len(jobs) != 1 {
			t.Fatalf("jobs = %v, want one", jobs)
		}
		wantRandomID, err := send.StableRandomID(jobs[0], "put")
		if err != nil {
			t.Fatal(err)
		}
		for _, control := range w.network.SentControls(info.ChannelID) {
			if strings.HasPrefix(control.Text, "TDX1|t=put|") && control.RandomID != wantRandomID {
				t.Fatalf("put random id = %d, want the job derived %d", control.RandomID, wantRandomID)
			}
		}
	})

	t.Run("an uncertain tombstone resolves through the sweeper", func(t *testing.T) {
		w := newWorld(t)
		info := w.createBucket("photos", "none")
		meta := w.mustPut("photos", "report.txt", []byte("quarterly"))
		lost := faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}
		if err := w.client.QueueFaults(faketg.MethodSendControlIdempotent, lost); err != nil {
			t.Fatalf("queue fault: %v", err)
		}
		err := w.write.Remove.Delete(w.callerContext(), "photos", "report.txt")
		if err == nil {
			t.Fatal("delete with an unknown tombstone outcome returned success")
		}
		if got := w.controlRows(`SELECT state FROM delete_jobs`); len(got) != 1 || got[0] != "uncertain_del" {
			t.Fatalf("delete job states = %v, want one uncertain_del", got)
		}
		w.clk.Advance(10 * time.Minute)
		w.write.Sweeper.RunOnce(context.Background())
		if got := w.count(`SELECT COUNT(*) FROM objects WHERE channel_id = ?1`, info.ChannelID); got != 0 {
			t.Fatalf("objects after reconciliation = %d, want 0", got)
		}
		if got := w.controlRows(`SELECT state FROM delete_jobs`); len(got) != 1 || got[0] != "committed" {
			t.Fatalf("delete job states = %v, want one committed", got)
		}
		if ids := w.messageIDs(info.ChannelID); ids[meta.PutMsgID] || ids[meta.BodyMsgID] {
			t.Fatalf("deleted object messages still exist in Telegram: %v", ids)
		}
	})
}

func countPutMessages(w *world, channelID int64) int {
	count := 0
	for _, control := range w.network.SentControls(channelID) {
		if strings.HasPrefix(control.Text, "TDX1|t=put|") {
			count++
		}
	}
	return count
}
