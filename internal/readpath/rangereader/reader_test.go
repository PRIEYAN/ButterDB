package rangereader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestRangeReaderSplitsAcrossUploadBoundaries(t *testing.T) {
	data := testBytes(blockBytes*2 + 257)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	off := int64(blockBytes - 20)
	buf := make([]byte, 80)
	n, err := reader.ReadStoredAt(context.Background(), ref, buf, off)
	if err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if n != len(buf) {
		t.Fatalf("n = %d, want %d", n, len(buf))
	}
	if !bytes.Equal(buf, data[off:off+int64(len(buf))]) {
		t.Fatal("read bytes mismatch")
	}

	calls := fake.calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2 block reads", calls)
	}
	if calls[0].offset != 0 || calls[0].length != blockBytes {
		t.Fatalf("first call = %+v, want first 1 MiB block", calls[0])
	}
	if calls[1].offset != int64(blockBytes) || calls[1].length != blockBytes {
		t.Fatalf("second call = %+v, want second 1 MiB block", calls[1])
	}
}

func TestRangeReaderEOFReaderAtSemantics(t *testing.T) {
	data := testBytes(128)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	buf := make([]byte, 20)
	n, err := reader.ReadStoredAt(context.Background(), ref, buf, 120)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if n != 8 {
		t.Fatalf("n = %d, want 8", n)
	}
	if !bytes.Equal(buf[:n], data[120:]) {
		t.Fatal("tail bytes mismatch")
	}

	n, err = reader.ReadStoredAt(context.Background(), ref, buf, int64(len(data)))
	if !errors.Is(err, io.EOF) || n != 0 {
		t.Fatalf("at EOF n=%d err=%v, want 0/io.EOF", n, err)
	}
	if calls := fake.calls(); len(calls) != 1 || calls[0].length != 128 {
		t.Fatalf("final block call = %+v, want one 128-byte non-4KiB tail read", calls)
	}
}

func TestRangeReaderReadsLargeOffsetsWithoutLargeAllocation(t *testing.T) {
	const threeGiB = int64(3) * 1024 * 1024 * 1024
	fake := &largeSparseRangeFake{size: threeGiB + 2*int64(blockBytes)}
	reader := startReader(t, fake, RangeReaderConfig{})

	off := threeGiB + 33*1024
	buf := make([]byte, 512)
	n, err := reader.ReadStoredAt(context.Background(), fake.ref(), buf, off)
	if err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if n != len(buf) {
		t.Fatalf("n = %d, want %d", n, len(buf))
	}
	for i, b := range buf {
		if want := sparseByte(off + int64(i)); b != want {
			t.Fatalf("buf[%d] = %d, want %d", i, b, want)
		}
	}
	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one block read", calls)
	}
	if calls[0].offset < threeGiB || calls[0].length != blockBytes {
		t.Fatalf("call = %+v, want one 1 MiB block beyond 3 GiB", calls[0])
	}
}

func TestRangeReaderCachesBlocks(t *testing.T) {
	data := testBytes(blockBytes + 1)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()
	const off = openingChunkBytes + 100
	for i := range 2 {
		buf := make([]byte, 64)
		n, err := reader.ReadStoredAt(t.Context(), ref, buf, off)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if n != len(buf) || !bytes.Equal(buf, data[off:off+int64(len(buf))]) {
			t.Fatalf("read %d: n = %d, want %d matching bytes", i, n, len(buf))
		}
	}
	if calls := fake.calls(); len(calls) != 1 || calls[0].offset != 0 || calls[0].length != blockBytes {
		t.Fatalf("calls = %+v, want one cached block fetch", calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.ReadStoredAt(ctx, ref, make([]byte, 64), off); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached canceled read err = %v, want context.Canceled", err)
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("canceled cached read made new calls = %+v", calls)
	}
}

func TestRangeReaderPrefetchesNextBlockWhenEnabled(t *testing.T) {
	data := testBytes(blockBytes*3 + 1)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{ReadAhead: 1})
	buf := make([]byte, 64)
	if _, err := reader.ReadStoredAt(context.Background(), fake.ref(), buf, openingChunkBytes+128); err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		calls := fake.calls()
		if len(calls) >= 2 {
			if calls[0].offset != 0 {
				t.Fatalf("first call = %+v, want foreground block 0", calls[0])
			}
			if calls[1].offset != int64(blockBytes) {
				t.Fatalf("second call = %+v, want prefetched next block", calls[1])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("calls = %+v, want foreground + prefetch", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRangeReaderCoalescesConcurrentBlockReads(t *testing.T) {
	data := testBytes(blockBytes)
	fake := newStrictRangeFake(data)
	fake.delay = 20 * time.Millisecond
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()
	const off = openingChunkBytes + 512
	const readers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Go(func() {
			<-start
			buf := make([]byte, 32)
			_, err := reader.ReadStoredAt(context.Background(), ref, buf, off)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(buf, data[off:off+32]) {
				errs <- fmt.Errorf("bytes mismatch")
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want one coalesced block fetch", calls)
	}
}

func TestRangeReaderCallerCancellationDoesNotPoisonCoalescedWaiter(t *testing.T) {
	data := testBytes(blockBytes)
	fake := newStrictRangeFake(data)
	fake.entered = make(chan struct{})
	fake.release = make(chan struct{})
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()
	const off = openingChunkBytes

	ctx1, cancel1 := context.WithCancel(context.Background())
	err1 := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(ctx1, ref, make([]byte, 32), off)
		err1 <- err
	}()

	select {
	case <-fake.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first block fetch")
	}

	buf2 := make([]byte, 32)
	err2 := make(chan error, 1)
	go func() {
		_, err := reader.ReadStoredAt(context.Background(), ref, buf2, off)
		err2 <- err
	}()

	cancel1()
	select {
	case err := <-err1:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled first caller")
	}

	close(fake.release)
	select {
	case err := <-err2:
		if err != nil {
			t.Fatalf("second read: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced second caller")
	}
	if !bytes.Equal(buf2, data[off:off+32]) {
		t.Fatal("second caller bytes mismatch")
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want one shared block fetch", calls)
	}
}
func TestRangeReaderForegroundJoinsBackgroundFlightOnTheWire(t *testing.T) {
	data := testBytes(blockBytes)
	fake := &priorityRangeFake{
		data:      data,
		bgEntered: make(chan struct{}),
		bgRelease: make(chan struct{}),
	}
	reader := startReader(t, fake, RangeReaderConfig{})
	ref := fake.ref()

	reader.prefetchBlock(ref, 0)
	select {
	case <-fake.bgEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background block fetch")
	}

	type result struct {
		block []byte
		err   error
	}
	foreground := make(chan result, 1)
	go func() {
		block, err := reader.block(context.Background(), ref, 0)
		foreground <- result{block: block, err: err}
	}()

	select {
	case res := <-foreground:
		t.Fatalf("foreground read finished before the shared fetch: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want the foreground read to join the single fetch", calls)
	}

	close(fake.bgRelease)
	select {
	case res := <-foreground:
		if res.err != nil {
			t.Fatalf("foreground block: %v", res.err)
		}
		if !bytes.Equal(res.block, data) {
			t.Fatal("foreground block bytes mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for foreground block")
	}
	if calls := fake.calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want one shared fetch", calls)
	}
}

func TestRangeReaderRetriesFloodWait(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.floodWaits = 2
	fake.floodWait = time.Millisecond
	var waits int
	reader := startReader(t, fake, RangeReaderConfig{
		OnFloodWait: func(time.Duration) {
			waits++
		},
	})
	ref := fake.ref()

	buf := make([]byte, 64)
	if _, err := reader.ReadStoredAt(context.Background(), ref, buf, 0); err != nil {
		t.Fatalf("ReadStoredAt: %v", err)
	}
	if calls := fake.calls(); len(calls) != 3 {
		t.Fatalf("calls = %+v, want 3 attempts", calls)
	}
	if waits != 2 {
		t.Fatalf("wait hooks = %d, want 2", waits)
	}
}

func TestRangeReaderCancellationDuringFloodWait(t *testing.T) {
	data := testBytes(1024)
	fake := newStrictRangeFake(data)
	fake.floodWaits = 10
	fake.floodWait = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	reader := startReaderSleeping(t, fake, RangeReaderConfig{
		OnFloodWait: func(time.Duration) {
			cancel()
		},
	}, SleeperFunc(func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	ref := fake.ref()

	_, err := reader.ReadStoredAt(ctx, ref, make([]byte, 64), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRangeReaderEvictsLeastRecentlyUsedBlock(t *testing.T) {
	data := testBytes(blockBytes * 2)
	fake := newStrictRangeFake(data)
	reader := startReader(t, fake, RangeReaderConfig{
		MaxCacheBytes: int64(blockBytes),
	})
	ref := fake.ref()
	for _, off := range []int64{openingChunkBytes, int64(blockBytes), openingChunkBytes} {
		if _, err := reader.ReadStoredAt(context.Background(), ref, make([]byte, 16), off); err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
	}
	calls := fake.calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want block0, block1, block0 after eviction", calls)
	}
	if calls[0].offset != 0 || calls[1].offset != int64(blockBytes) || calls[2].offset != 0 {
		t.Fatalf("call order = %+v", calls)
	}
}

type rangeCall struct {
	offset int64
	length int
}

type strictRangeFake struct {
	mu                        sync.Mutex
	data                      []byte
	callLog                   []rangeCall
	delay                     time.Duration
	floodWaits                int
	floodWait                 time.Duration
	transientFailures         int
	transientFailuresByOffset map[int64]int
	shortReadFailures         int
	fullReadEOFFailures       int
	failErr                   error
	entered                   chan struct{}
	release                   chan struct{}
	enterOnce                 sync.Once
	refVersion                byte
	resolves                  int
}

func newStrictRangeFake(data []byte) *strictRangeFake {
	return &strictRangeFake{data: data}
}

func (f *strictRangeFake) ref() tgclient.DocumentRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refLocked()
}

func (f *strictRangeFake) refLocked() tgclient.DocumentRef {
	return tgclient.DocumentRef{
		Peer:          tgclient.InputPeer{ChannelID: 42},
		MsgID:         99,
		Size:          int64(len(f.data)),
		FileReference: []byte{f.refVersion},
	}
}
func (f *strictRangeFake) expireReference() {
	f.mu.Lock()
	f.refVersion++
	f.mu.Unlock()
}

func (f *strictRangeFake) resolveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolves
}

func (f *strictRangeFake) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	return f.refLocked(), nil
}

func (f *strictRangeFake) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if err := validateNormalizedRange(offset, len(dst)); err != nil {
		return 0, err
	}

	f.mu.Lock()
	f.callLog = append(f.callLog, rangeCall{offset: offset, length: len(dst)})
	if f.entered != nil {
		f.enterOnce.Do(func() { close(f.entered) })
	}
	if len(ref.FileReference) != 1 || ref.FileReference[0] != f.refVersion {
		f.mu.Unlock()
		return 0, tgclient.ErrFileReferenceExpired
	}
	if f.floodWaits > 0 {
		f.floodWaits--
		wait := f.floodWait
		f.mu.Unlock()
		return 0, &tgclient.FloodWaitError{Duration: wait}
	}
	if failures := f.transientFailuresByOffset[offset]; failures > 0 {
		f.transientFailuresByOffset[offset] = failures - 1
		f.mu.Unlock()
		return 0, strictRangeTransientError()
	}
	if f.transientFailures > 0 {
		f.transientFailures--
		f.mu.Unlock()
		return 0, strictRangeTransientError()
	}
	if f.failErr != nil {
		err := f.failErr
		f.mu.Unlock()
		return 0, err
	}
	shortRead := f.shortReadFailures > 0
	if shortRead {
		f.shortReadFailures--
	}
	fullReadEOF := f.fullReadEOFFailures > 0
	if fullReadEOF {
		f.fullReadEOFFailures--
	}
	delay := f.delay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if ref.MsgID != 99 {
		return 0, tgclient.ErrMessageNotFound
	}
	if offset < 0 || offset+int64(len(dst)) > int64(len(f.data)) {
		return 0, io.ErrUnexpectedEOF
	}
	if shortRead {
		n := len(dst) / 2
		if n == 0 {
			return 0, nil
		}
		return copy(dst[:n], f.data[offset:offset+int64(n)]), nil
	}
	if fullReadEOF {
		return copy(dst, f.data[offset:offset+int64(len(dst))]), io.EOF
	}
	return copy(dst, f.data[offset:offset+int64(len(dst))]), nil
}

func (f *strictRangeFake) calls() []rangeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rangeCall(nil), f.callLog...)
}

type priorityRangeFake struct {
	mu        sync.Mutex
	data      []byte
	callLog   []rangeCall
	bgEntered chan struct{}
	bgRelease chan struct{}
}

func (f *priorityRangeFake) ref() tgclient.DocumentRef {
	return tgclient.DocumentRef{
		Peer:  tgclient.InputPeer{ChannelID: 42},
		MsgID: 99,
		Size:  int64(len(f.data)),
	}
}

func (f *priorityRangeFake) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	return f.ref(), nil
}

func (f *priorityRangeFake) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if err := validateNormalizedRange(offset, len(dst)); err != nil {
		return 0, err
	}
	f.mu.Lock()
	f.callLog = append(f.callLog, rangeCall{offset: offset, length: len(dst)})
	callIndex := len(f.callLog)
	f.mu.Unlock()

	if callIndex == 1 {
		close(f.bgEntered)
		select {
		case <-f.bgRelease:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if ref.MsgID != 99 {
		return 0, tgclient.ErrMessageNotFound
	}
	if offset < 0 || offset+int64(len(dst)) > int64(len(f.data)) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(dst, f.data[offset:offset+int64(len(dst))]), nil
}

func (f *priorityRangeFake) calls() []rangeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rangeCall(nil), f.callLog...)
}

func validateNormalizedRange(offset int64, length int) error {
	if length <= 0 {
		return fmt.Errorf("empty range request")
	}
	if offset%alignmentBytes != 0 {
		return fmt.Errorf("offset %d is not %d-aligned", offset, alignmentBytes)
	}
	if length > blockBytes {
		return fmt.Errorf("range length %d exceeds max %d", length, blockBytes)
	}
	startBucket := offset / int64(blockBytes)
	endBucket := (offset + int64(length) - 1) / int64(blockBytes)
	if startBucket != endBucket {
		return fmt.Errorf("range crosses 1 MiB boundary")
	}
	return nil
}

func testBytes(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = sparseByte(int64(i))
	}
	return data
}

type largeSparseRangeFake struct {
	mu      sync.Mutex
	size    int64
	callLog []rangeCall
}

func (f *largeSparseRangeFake) ref() tgclient.DocumentRef {
	return tgclient.DocumentRef{
		MsgID: 99,
		Size:  f.size,
	}
}

func (f *largeSparseRangeFake) ResolveDocument(context.Context, tgclient.InputPeer, int64) (tgclient.DocumentRef, error) {
	return f.ref(), nil
}

func (f *largeSparseRangeFake) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateNormalizedRange(offset, len(dst)); err != nil {
		return 0, err
	}
	if ref.MsgID != 99 {
		return 0, tgclient.ErrMessageNotFound
	}
	if offset < 0 || offset+int64(len(dst)) > f.size {
		return 0, io.ErrUnexpectedEOF
	}

	f.mu.Lock()
	f.callLog = append(f.callLog, rangeCall{offset: offset, length: len(dst)})
	f.mu.Unlock()

	for i := range dst {
		dst[i] = sparseByte(offset + int64(i))
	}
	return len(dst), nil
}

func (f *largeSparseRangeFake) calls() []rangeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rangeCall(nil), f.callLog...)
}

func sparseByte(offset int64) byte {
	return byte((offset * 31) % 251)
}
