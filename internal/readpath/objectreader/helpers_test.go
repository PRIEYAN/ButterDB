package objectreader

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	testChannelID        int64 = 424242
	testKeyID                  = "00112233445566ff"
	resolverEventTimeout       = 10 * time.Second
	encryptedTestChunk         = format.ChunkSize
)

func testPeer() tgclient.InputPeer {
	return tgclient.InputPeer{ChannelID: testChannelID, AccessHash: 99}
}

func peerForChannel(channelID int64) tgclient.InputPeer {
	return tgclient.InputPeer{ChannelID: channelID, AccessHash: channelID*10 + 1}
}

type fakeTelegram struct {
	mu          sync.Mutex
	peer        tgclient.InputPeer
	bodies      map[int64][]byte
	resolved    []int64
	readMsgIDs  []int64
	resolveHook func(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error)
}

func newFakeTelegram(bodies map[int64][]byte) *fakeTelegram {
	return &fakeTelegram{peer: testPeer(), bodies: bodies}
}

func (f *fakeTelegram) ResolvePeer(context.Context, int64) (tgclient.InputPeer, error) {
	return f.peer, nil
}

func (f *fakeTelegram) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	f.mu.Lock()
	f.resolved = append(f.resolved, msgID)
	hook := f.resolveHook
	body, ok := f.bodies[msgID]
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, peer, msgID)
	}
	if !ok {
		return tgclient.DocumentRef{}, tgclient.ErrMessageNotFound
	}
	return tgclient.DocumentRef{Peer: peer, MsgID: msgID, Size: int64(len(body)), FileReference: []byte{1}}, nil
}

func (f *fakeTelegram) ReadStoredAt(_ context.Context, ref tgclient.DocumentRef, dst []byte, off int64) (int, error) {
	f.mu.Lock()
	f.readMsgIDs = append(f.readMsgIDs, ref.MsgID)
	body, ok := f.bodies[ref.MsgID]
	f.mu.Unlock()
	if !ok {
		return 0, tgclient.ErrMessageNotFound
	}
	if off < 0 || off+int64(len(dst)) > int64(len(body)) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(dst, body[off:off+int64(len(dst))]), nil
}

func (f *fakeTelegram) resolvedMessages() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.resolved...)
}

func (f *fakeTelegram) readMessages() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.readMsgIDs...)
}

type testDEK struct {
	mu      sync.Mutex
	value   []byte
	cleared int
}

func newTestDEK(value []byte) *testDEK {
	return &testDEK{value: value}
}

func (d *testDEK) Bytes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.value
}

func (d *testDEK) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	clear(d.value)
	d.cleared++
}

type keyFunc func(ctx context.Context, bucketChannelID int64, keyID string) (DEK, error)

func (f keyFunc) DEK(ctx context.Context, bucketChannelID int64, keyID string) (DEK, error) {
	return f(ctx, bucketChannelID, keyID)
}

func fixedKey(key []byte) KeyProvider {
	return keyFunc(func(context.Context, int64, string) (DEK, error) {
		return newTestDEK(append([]byte(nil), key...)), nil
	})
}

func unavailableKeys(err error) KeyProvider {
	return keyFunc(func(context.Context, int64, string) (DEK, error) { return nil, err })
}

func testDependencies(tg *fakeTelegram, keys KeyProvider) Dependencies {
	return Dependencies{
		Peers:     tg,
		Documents: tg,
		Stored:    tg,
		Keys:      keys,
		Clock:     clock.NewFake(time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)),
	}
}

func closeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), resolverEventTimeout)
}

func newTestOpener(t *testing.T, deps Dependencies) *Opener {
	t.Helper()
	opener, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := closeContext()
		defer cancel()
		_ = opener.Close(ctx)
	})
	return opener
}

func newDefaultOpener(t *testing.T, tg *fakeTelegram, keys KeyProvider) *Opener {
	t.Helper()
	if keys == nil {
		keys = unavailableKeys(nil)
	}
	return newTestOpener(t, testDependencies(tg, keys))
}

func newTestSource(t *testing.T, deps SourceDependencies) *StoredSource {
	t.Helper()
	source, err := NewStoredSource(deps)
	if err != nil {
		t.Fatalf("NewStoredSource: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := closeContext()
		defer cancel()
		_ = source.Close(ctx)
	})
	return source
}

func plainMeta(bodyMsgID int64, body []byte) object.ObjectMeta {
	return object.ObjectMeta{
		ChannelID:     testChannelID,
		Key:           "notes.txt",
		PutMsgID:      bodyMsgID + 1,
		BodyMsgID:     bodyMsgID,
		StoredSize:    int64(len(body)),
		PlaintextSize: int64(len(body)),
		ETag:          "d41d8cd98f00b204e9800998ecf8427e",
	}
}

func encryptedMeta(bodyMsgID int64, ciphertext []byte, plaintextSize int) object.ObjectMeta {
	return object.ObjectMeta{
		ChannelID:         testChannelID,
		Key:               "private.bin",
		PutMsgID:          bodyMsgID + 1,
		BodyMsgID:         bodyMsgID,
		StoredSize:        int64(len(ciphertext)),
		PlaintextSize:     int64(plaintextSize),
		ETag:              "d41d8cd98f00b204e9800998ecf8427e",
		Encrypted:         true,
		EncryptionVersion: 1,
		KeyID:             testKeyID,
	}
}

func encryptFixture(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	var ciphertext bytes.Buffer
	var salt [format.SaltLen]byte
	var prefix [format.NoncePrefixLen]byte
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	for i := range prefix {
		prefix[i] = byte(0xa0 + i)
	}
	if err := stream.EncryptStream(&ciphertext, bytes.NewReader(plaintext), key, int64(len(plaintext)), salt, prefix); err != nil {
		t.Fatalf("EncryptStream: %v", err)
	}
	return ciphertext.Bytes()
}

func patternedPlaintext(size int) []byte {
	plaintext := make([]byte, size)
	for index := range plaintext {
		plaintext[index] = byte((index*19 + 5) % 251)
	}
	return plaintext
}

func zeroed(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}

func mustOpenReader(t *testing.T, opener *Opener, meta object.ObjectMeta) *Reader {
	t.Helper()
	opened, err := opener.Open(context.Background(), meta)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	reader, ok := opened.(*Reader)
	if !ok {
		t.Fatalf("Open returned %T, want *Reader", opened)
	}
	return reader
}

func waitForSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(resolverEventTimeout):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitForResolveEvent(t *testing.T, events <-chan int64) int64 {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(resolverEventTimeout):
		t.Fatal("timed out waiting for resolver event")
		return 0
	}
}

func waitForResolveStarts(t *testing.T, started <-chan int64, count int) {
	t.Helper()
	for range count {
		_ = waitForResolveEvent(t, started)
	}
}

func waitForOpenResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(resolverEventTimeout):
		t.Fatal("timed out waiting for Open")
		return nil
	}
}
