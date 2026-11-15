package objectreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestOpenerReadsSingleFileRanges(t *testing.T) {
	body := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	tg := newFakeTelegram(map[int64][]byte{10: body})
	opener := newDefaultOpener(t, tg, nil)

	reader := mustOpenReader(t, opener, plainMeta(10, body))
	if reader.Size() != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", reader.Size(), len(body))
	}
	got := make([]byte, 8)
	n, err := reader.ReadAt(context.Background(), got, 7)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(got) || !bytes.Equal(got, body[7:15]) {
		t.Fatalf("ReadAt = %q (%d), want %q", got, n, body[7:15])
	}
}

func TestReaderUsesReaderAtEOFContract(t *testing.T) {
	body := []byte("tail")
	tg := newFakeTelegram(map[int64][]byte{10: body})
	opener := newDefaultOpener(t, tg, nil)
	reader := mustOpenReader(t, opener, plainMeta(10, body))

	got := make([]byte, 8)
	n, err := reader.ReadAt(context.Background(), got, 2)
	if n != 2 || !errors.Is(err, io.EOF) || string(got[:n]) != "il" {
		t.Fatalf("tail read = %q n=%d err=%v, want il/2/EOF", got[:n], n, err)
	}
	n, err = reader.ReadAt(context.Background(), got, int64(len(body)))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF read n=%d err=%v, want 0/EOF", n, err)
	}
}

func TestOpenerRejectsEncryptedFilesWithoutKeyProvider(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	plaintext := []byte("private")
	ciphertext := encryptFixture(t, plaintext, key)
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})
	opener := newDefaultOpener(t, tg, unavailableKeys(errs.ErrKeyUnavailable))

	opened, err := opener.Open(context.Background(), encryptedMeta(10, ciphertext, len(plaintext)))
	if opened != nil || !errors.Is(err, errs.ErrKeyUnavailable) {
		t.Fatalf("Open encrypted = %v, %v, want nil/ErrKeyUnavailable", opened, err)
	}
}

func TestNewValidatesDependencies(t *testing.T) {
	complete := testDependencies(newFakeTelegram(nil), fixedKey(nil))
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{name: "peers", mutate: func(d *Dependencies) { d.Peers = nil }},
		{name: "documents", mutate: func(d *Dependencies) { d.Documents = nil }},
		{name: "stored reader", mutate: func(d *Dependencies) { d.Stored = nil }},
		{name: "keys", mutate: func(d *Dependencies) { d.Keys = nil }},
		{name: "clock", mutate: func(d *Dependencies) { d.Clock = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := complete
			tt.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("New error = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := NewStoredSource(SourceDependencies{}); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("NewStoredSource error = %v, want ErrMissingDependency", err)
	}
}

func TestOpenerRejectsClosedAndCanceledOperations(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("x")})
	opener := newDefaultOpener(t, tg, nil)
	meta := plainMeta(10, []byte("x"))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := opener.Open(canceled, meta); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open canceled err = %v, want context.Canceled", err)
	}

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := opener.Open(context.Background(), meta); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open closed err = %v, want ErrClosed", err)
	}
	var nilOpener *Opener
	if _, err := nilOpener.Open(context.Background(), meta); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Open err = %v, want ErrClosed", err)
	}
}

func TestOpenerValidatesResolvedTelegramMetadata(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("five!")})
	opener := newDefaultOpener(t, tg, nil)

	mismatched := plainMeta(10, []byte("four"))
	if _, err := opener.Open(context.Background(), mismatched); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("Open size mismatch err = %v, want ErrDocumentMismatch", err)
	}
	if _, err := opener.Open(context.Background(), plainMeta(999, []byte("x"))); !errors.Is(err, tgclient.ErrMessageNotFound) {
		t.Fatalf("Open missing err = %v, want ErrMessageNotFound", err)
	}
}

func TestReaderValidatesOffsetsAndContext(t *testing.T) {
	tg := newFakeTelegram(map[int64][]byte{10: []byte("abc")})
	opener := newDefaultOpener(t, tg, nil)
	reader := mustOpenReader(t, opener, plainMeta(10, []byte("abc")))

	if n, err := reader.ReadAt(context.Background(), nil, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt n=%d err=%v, want 0/nil", n, err)
	}
	if _, err := reader.ReadAt(context.Background(), make([]byte, 1), -1); !errors.Is(err, ErrNegativeOffset) {
		t.Fatalf("negative ReadAt error = %v, want ErrNegativeOffset", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ReadAt(canceled, make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ReadAt err = %v, want context.Canceled", err)
	}
	if size := (*Reader)(nil).Size(); size != 0 {
		t.Fatalf("nil Reader Size = %d, want 0", size)
	}
}

func TestReaderPinsVersion(t *testing.T) {
	oldBody := []byte("old version bytes")
	newBody := []byte("new version bytes, longer")
	tg := newFakeTelegram(map[int64][]byte{10: oldBody})
	opener := newDefaultOpener(t, tg, nil)
	oldMeta := plainMeta(10, oldBody)
	reader := mustOpenReader(t, opener, oldMeta)

	tg.mu.Lock()
	tg.bodies[20] = newBody
	tg.mu.Unlock()
	overwritten := plainMeta(20, newBody)
	overwritten.Key = oldMeta.Key
	overwritten.PutMsgID = oldMeta.PutMsgID + 100

	got := make([]byte, len(oldBody))
	n, err := reader.ReadAt(context.Background(), got, 0)
	if err != nil || n != len(oldBody) || !bytes.Equal(got, oldBody) {
		t.Fatalf("pinned ReadAt = %q n=%d err=%v, want the old version", got[:n], n, err)
	}
	if reader.Size() != int64(len(oldBody)) {
		t.Fatalf("pinned Size = %d, want %d", reader.Size(), len(oldBody))
	}
	for _, msgID := range tg.readMessages() {
		if msgID != 10 {
			t.Fatalf("pinned reader read message %d, want only 10", msgID)
		}
	}

	fresh := mustOpenReader(t, opener, overwritten)
	freshBytes := make([]byte, len(newBody))
	if n, err := fresh.ReadAt(context.Background(), freshBytes, 0); err != nil || n != len(newBody) || !bytes.Equal(freshBytes, newBody) {
		t.Fatalf("fresh reader = %q n=%d err=%v, want the new version", freshBytes[:n], n, err)
	}
}

func TestReaderWithContextIsAnIOReaderAt(t *testing.T) {
	body := []byte("section reader body")
	tg := newFakeTelegram(map[int64][]byte{10: body})
	opener := newDefaultOpener(t, tg, nil)
	reader := mustOpenReader(t, opener, plainMeta(10, body))

	section := io.NewSectionReader(reader.WithContext(context.Background()), 8, 6)
	got, err := io.ReadAll(section)
	if err != nil || string(got) != "reader" {
		t.Fatalf("section = %q, %v, want reader", got, err)
	}
}

func TestOpenerOpensZeroByteObjectWithoutBody(t *testing.T) {
	tg := newFakeTelegram(nil)
	opener := newDefaultOpener(t, tg, nil)
	meta := object.ObjectMeta{ChannelID: testChannelID, Key: "empty", PutMsgID: 5}

	reader := mustOpenReader(t, opener, meta)
	if n, err := reader.ReadAt(context.Background(), make([]byte, 4), 0); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("empty object ReadAt n=%d err=%v, want 0/EOF", n, err)
	}
	if got := tg.resolvedMessages(); len(got) != 0 {
		t.Fatalf("zero byte object resolved messages %v, want none", got)
	}
}

func TestSourceAcceptsFullReadReportedWithEOF(t *testing.T) {
	body := []byte("eof flavoured")
	tg := newFakeTelegram(map[int64][]byte{10: body})
	source := newTestSource(t, SourceDependencies{
		Peers:     tg,
		Documents: tg,
		Stored:    eofFlavouredStored{tg: tg},
		Clock:     testDependencies(tg, nil).Clock,
	})

	got := make([]byte, 4)
	n, err := source.ReadDocumentRange(context.Background(), plainMeta(10, body), int64(len(body)-4), got)
	if err != nil || n != 4 || string(got) != "ured" {
		t.Fatalf("ReadDocumentRange = %q n=%d err=%v, want ured/4/nil", got, n, err)
	}
}

func TestSourceReportsShortStoredReadAsUnexpectedEOF(t *testing.T) {
	body := []byte("short")
	tg := newFakeTelegram(map[int64][]byte{10: body})
	source := newTestSource(t, SourceDependencies{
		Peers:     tg,
		Documents: tg,
		Stored:    shortStored{},
		Clock:     testDependencies(tg, nil).Clock,
	})

	n, err := source.ReadDocumentRange(context.Background(), plainMeta(10, body), 0, make([]byte, 5))
	if n != 2 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadDocumentRange n=%d err=%v, want 2/ErrUnexpectedEOF", n, err)
	}
}

func TestValidateMetaRejectsInvalidAndOverflowingMetadata(t *testing.T) {
	ciphertextFor := func(plaintext int64) int64 { return format.CiphertextSize(plaintext) }
	tests := []struct {
		name    string
		meta    object.ObjectMeta
		wantErr error
	}{
		{name: "negative stored size", meta: object.ObjectMeta{StoredSize: -1, PlaintextSize: -1}, wantErr: ErrInvalidMeta},
		{name: "negative plaintext size", meta: object.ObjectMeta{StoredSize: 1, PlaintextSize: -1, BodyMsgID: 1}, wantErr: ErrInvalidMeta},
		{name: "negative body message", meta: object.ObjectMeta{StoredSize: 1, PlaintextSize: 1, BodyMsgID: -1}, wantErr: ErrInvalidMeta},
		{name: "plain size mismatch", meta: object.ObjectMeta{StoredSize: 2, PlaintextSize: 3, BodyMsgID: 1}, wantErr: ErrInvalidMeta},
		{name: "bytes without body", meta: object.ObjectMeta{StoredSize: 2, PlaintextSize: 2}, wantErr: ErrInvalidMeta},
		{
			name:    "unsupported encryption version",
			meta:    object.ObjectMeta{StoredSize: ciphertextFor(1), PlaintextSize: 1, BodyMsgID: 1, Encrypted: true, EncryptionVersion: 2, KeyID: testKeyID},
			wantErr: ErrEncryptedUnsupported,
		},
		{
			name:    "stored size does not match ciphertext arithmetic",
			meta:    object.ObjectMeta{StoredSize: ciphertextFor(1) + 1, PlaintextSize: 1, BodyMsgID: 1, Encrypted: true, EncryptionVersion: 1, KeyID: testKeyID},
			wantErr: randomaccess.ErrCiphertextSize,
		},
		{
			name:    "plaintext beyond tde1 capacity",
			meta:    object.ObjectMeta{StoredSize: math.MaxInt64, PlaintextSize: math.MaxInt64, BodyMsgID: 1, Encrypted: true, EncryptionVersion: 1, KeyID: testKeyID},
			wantErr: format.ErrPlaintextTooLarge,
		},
		{
			name:    "encrypted without body",
			meta:    object.ObjectMeta{StoredSize: ciphertextFor(1), PlaintextSize: 1, Encrypted: true, EncryptionVersion: 1, KeyID: testKeyID},
			wantErr: ErrInvalidMeta,
		},
		{
			name:    "encrypted without key id",
			meta:    object.ObjectMeta{StoredSize: ciphertextFor(1), PlaintextSize: 1, BodyMsgID: 1, Encrypted: true, EncryptionVersion: 1},
			wantErr: ErrInvalidMeta,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateMeta(tt.meta); !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateMeta error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

type eofFlavouredStored struct {
	tg *fakeTelegram
}

func (s eofFlavouredStored) ReadStoredAt(ctx context.Context, ref tgclient.DocumentRef, dst []byte, off int64) (int, error) {
	n, err := s.tg.ReadStoredAt(ctx, ref, dst, off)
	if err != nil {
		return n, err
	}
	return n, io.EOF
}

type shortStored struct{}

func (shortStored) ReadStoredAt(context.Context, tgclient.DocumentRef, []byte, int64) (int, error) {
	return 2, nil
}
