package put

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func decryptBody(t *testing.T, sealed []byte) []byte {
	t.Helper()
	var plain bytes.Buffer
	if _, err := stream.DecryptStream(&plain, bytes.NewReader(sealed), testDEK); err != nil {
		t.Fatalf("decrypt stored body: %v", err)
	}
	return plain.Bytes()
}

func TestEncryptedPutStoresTDE1Body(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	plain := bytes.Repeat([]byte("secret body "), 9000)
	meta := mustPut(t, w, w.putRequest("secret.bin", len(plain)), plain)

	job := w.jobs.only()
	if !job.Encrypted || job.KeyID != testKeyID || len(job.FileSalt) != 16 || len(job.NoncePrefix) != 20 || job.EncryptionVersion != 1 {
		t.Fatalf("job = %+v", job)
	}
	if job.StoredSize != format.CiphertextSize(int64(len(plain))) || job.PlaintextSize != int64(len(plain)) {
		t.Fatalf("sizes stored=%d plain=%d", job.StoredSize, job.PlaintextSize)
	}
	files := w.sentFiles()
	if len(files) != 1 || files[0].Size != job.StoredSize {
		t.Fatalf("sent = %+v", files)
	}
	op, err := wire.Parse(wire.ExtractHeaderLine(files[0].Caption))
	if err != nil || !op.Encrypted || op.StoredSize != job.StoredSize {
		t.Fatalf("body caption op = %+v err=%v", op, err)
	}
	stored := w.readBody(meta.BodyMsgID)
	if bytes.Contains(stored, []byte("secret body")) {
		t.Fatal("plaintext reached Telegram")
	}
	if !bytes.Equal(decryptBody(t, stored), plain) {
		t.Fatal("stored body does not decrypt to the uploaded bytes")
	}
	live, ok := w.liveObject("secret.bin")
	sum := md5.Sum(plain)
	if !ok || !live.Encrypted || live.KeyID != testKeyID || live.PlaintextSize != int64(len(plain)) || live.ETag != object.SingleETag(sum) {
		t.Fatalf("live = %+v ok=%v", live, ok)
	}
	if w.authority.ensured != 1 {
		t.Fatalf("EnsureKey calls = %d, want 1", w.authority.ensured)
	}
	for _, dek := range w.unwrap.deks {
		if !dek.cleared {
			t.Fatal("a DEK copy was not cleared")
		}
	}
	if len(w.unwrap.deks) == 0 {
		t.Fatal("no DEK was unwrapped")
	}
}

func TestEncryptedSaltAndNoncePersistedBeforeFirstSend(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	var atFirstSend jobrepo.UploadJob
	w.sender.fileHook = func(call int, _ tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
		if call == 1 {
			atFirstSend = w.jobs.only()
		}
		return tgclient.SendFileResult{}, nil, false
	}
	mustPut(t, w, w.putRequest("k", 3), []byte("abc"))
	if atFirstSend.State != jobrepo.UploadBodySending || len(atFirstSend.FileSalt) != 16 || len(atFirstSend.NoncePrefix) != 20 {
		t.Fatalf("job at first send = %s salt %d prefix %d", atFirstSend.State, len(atFirstSend.FileSalt), len(atFirstSend.NoncePrefix))
	}
}

func TestEncryptedRetryReusesSaltAndNonce(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	plain := bytes.Repeat([]byte("retry "), 5000)
	lost := faketg.Fault{Kind: faketg.FaultOutcomeUnknown}
	if err := w.telegram.QueueFaults(faketg.MethodSendFileIdempotent, lost, lost, lost); err != nil {
		t.Fatal(err)
	}
	var firstAttempt []byte
	w.sender.fileHook = func(call int, req tgclient.SendFileRequest) (tgclient.SendFileResult, error, bool) {
		if call == 1 {
			firstAttempt = readAllAndRewind(t, req)
		}
		return tgclient.SendFileResult{}, nil, false
	}

	_, err := w.svc.Put(w.ctx, w.putRequest("retry.bin", len(plain)), bytes.NewReader(plain))
	if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
		t.Fatalf("Put error = %v", err)
	}
	job := w.jobs.only()
	if job.State != jobrepo.UploadUncertainBody {
		t.Fatalf("job = %s", job.State)
	}
	salt, prefix := append([]byte(nil), job.FileSalt...), append([]byte(nil), job.NoncePrefix...)

	if err := w.svc.Resume(w.ctx, job.JobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	done := w.jobs.only()
	if !bytes.Equal(done.FileSalt, salt) || !bytes.Equal(done.NoncePrefix, prefix) {
		t.Fatal("a retry changed the persisted salt or nonce prefix")
	}
	stored := w.readBody(done.BodyMsgID)
	if !bytes.Equal(stored, firstAttempt) {
		t.Fatal("the retried body is a different ciphertext from the first attempt")
	}
	header, err := stream.ParseHeader(stored[:format.HeaderLen])
	if err != nil || !bytes.Equal(header.Salt[:], salt) || !bytes.Equal(header.NoncePrefix[:], prefix) {
		t.Fatalf("stored header = %+v err=%v, want the persisted parameters", header, err)
	}
	if !bytes.Equal(decryptBody(t, stored), plain) {
		t.Fatal("retried body does not decrypt")
	}
	if done.State != jobrepo.UploadCommitted {
		t.Fatalf("job = %s, want committed", done.State)
	}
}

func readAllAndRewind(t *testing.T, req tgclient.SendFileRequest) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(req.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if _, err := req.Body.Seek(0, 0); err != nil {
		t.Fatalf("rewind body: %v", err)
	}
	return buf.Bytes()
}

func TestEncryptionResolution(t *testing.T) {
	tests := []struct {
		name          string
		bucketDefault string
		requested     string
		wantEncrypted bool
		wantEnsure    int
	}{
		{name: "none default and no request", bucketDefault: bucket.EncryptionNone},
		{name: "none default and tde1 request", bucketDefault: bucket.EncryptionNone, requested: bucket.EncryptionTDE1, wantEncrypted: true, wantEnsure: 1},
		{name: "tde1 default and no request", bucketDefault: bucket.EncryptionTDE1, wantEncrypted: true, wantEnsure: 1},
		{name: "tde1 default and explicit none", bucketDefault: bucket.EncryptionTDE1, requested: bucket.EncryptionNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, tc.bucketDefault)
			req := w.putRequest("k", 3)
			req.Encryption = tc.requested
			meta := mustPut(t, w, req, []byte("abc"))
			if meta.Encrypted != tc.wantEncrypted || w.authority.ensured != tc.wantEnsure {
				t.Fatalf("encrypted=%v ensured=%d", meta.Encrypted, w.authority.ensured)
			}
		})
	}
}

func TestEncryptedPutRejectsPlaintextAboveTDE1CapacityBeforeSpool(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	req := w.putRequest("big", 0)
	req.ContentLength = limits.MaxEncryptedPlaintextBytes + 1
	if _, err := w.svc.Put(w.ctx, req, strings.NewReader("")); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("error = %v, want ErrObjectTooLarge", err)
	}
	if w.spool.writes != 0 || w.jobs.count() != 0 || len(w.sender.files) != 0 {
		t.Fatal("an over-capacity encrypted upload touched spool, jobs or Telegram")
	}
}

func TestEncryptedPutWithoutActiveKeyFails(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	deps := w.deps
	deps.Keys = noKeys{}
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Put(w.ctx, w.putRequest("k", 3), strings.NewReader("abc"))
	if !errors.Is(err, errs.ErrKeyUnavailable) {
		t.Fatalf("error = %v, want ErrKeyUnavailable", err)
	}
	if w.spool.writes != 0 || len(w.sender.files) != 0 {
		t.Fatal("nothing may be spooled or sent without a key")
	}
}

type noKeys struct{}

func (noKeys) ActiveKey(context.Context, int64, string) (EncryptionKey, bool, error) {
	return EncryptionKey{}, false, nil
}

func (noKeys) Key(context.Context, int64, string) (EncryptionKey, bool, error) {
	return EncryptionKey{}, false, nil
}

func TestEncryptedZeroByteObjectStillSendsBody(t *testing.T) {
	w := newWorld(t, bucket.EncryptionTDE1)
	meta := mustPut(t, w, w.putRequest("empty", 0), nil)
	files := w.sentFiles()
	if len(files) != 1 || files[0].Size != 66 || meta.StoredSize != 66 || meta.PlaintextSize != 0 {
		t.Fatalf("files=%+v meta=%+v, want the 66 byte TDE1 envelope", files, meta)
	}
	if got := decryptBody(t, w.readBody(meta.BodyMsgID)); len(got) != 0 {
		t.Fatalf("decrypted %d bytes, want none", len(got))
	}
}
