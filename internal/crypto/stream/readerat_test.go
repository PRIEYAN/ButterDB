package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func newReaderAtFixture(t *testing.T, plain, key []byte, params FileParams) *EncryptingReaderAt {
	t.Helper()
	reader, err := NewEncryptingReaderAt(bytes.NewReader(plain), int64(len(plain)), key, params.Salt, params.NoncePrefix)
	if err != nil {
		t.Fatalf("NewEncryptingReaderAt: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

func TestEncryptingReaderAtMatchesStream(t *testing.T) {
	key := testKey(0x51)
	params := testParams(t, 51)
	sizes := []int{0, 1, format.ChunkSize - 1, format.ChunkSize, format.ChunkSize + 1, format.ChunkSize*3 + 17, 1 << 20}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("size-%d", n), func(t *testing.T) {
			plain := deterministicBytes(t, int64(n)+1, n)
			want := encryptToBytes(t, plain, key, params)
			reader := newReaderAtFixture(t, plain, key, params)
			if reader.Size() != int64(len(want)) {
				t.Fatalf("Size = %d, want %d", reader.Size(), len(want))
			}
			got, err := io.ReadAll(io.NewSectionReader(reader, 0, reader.Size()))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("EncryptingReaderAt bytes differ from EncryptStream")
			}
			var decrypted bytes.Buffer
			if _, err := DecryptStream(&decrypted, bytes.NewReader(got), key); err != nil {
				t.Fatalf("DecryptStream of reader output: %v", err)
			}
			if !bytes.Equal(decrypted.Bytes(), plain) {
				t.Fatal("decrypted reader output differs from plaintext")
			}
		})
	}
}

func TestEncryptingReaderAtAnyOffset(t *testing.T) {
	key := testKey(0x52)
	params := testParams(t, 52)
	plain := deterministicBytes(t, 52, format.ChunkSize*2+4321)
	want := encryptToBytes(t, plain, key, params)
	reader := newReaderAtFixture(t, plain, key, params)
	total := int64(len(want))
	stride := int64(format.ChunkSize + format.TagLen)

	offsets := []int64{
		0, 1, format.HeaderLen - 1, format.HeaderLen, format.HeaderLen + 1,
		format.HeaderLen + stride - 1, format.HeaderLen + stride, format.HeaderLen + stride + 1,
		format.HeaderLen + 2*stride - 1, format.HeaderLen + 2*stride, total - 17, total - 1, total, total + 5,
	}
	lengths := []int{0, 1, 16, format.HeaderLen, format.ChunkSize, format.ChunkSize + format.TagLen + 9, 3 * format.ChunkSize}
	for _, off := range offsets {
		for _, length := range lengths {
			t.Run(fmt.Sprintf("off-%d-len-%d", off, length), func(t *testing.T) {
				got := make([]byte, length)
				wantBuf := make([]byte, length)
				n, err := reader.ReadAt(got, off)
				wantN, wantErr := bytes.NewReader(want).ReadAt(wantBuf, off)
				if n != wantN || !bytes.Equal(got[:n], wantBuf[:wantN]) {
					t.Fatalf("ReadAt n=%d err=%v, want n=%d err=%v", n, err, wantN, wantErr)
				}
				if (err == nil) != (wantErr == nil) || (err != nil && !errors.Is(err, io.EOF)) {
					t.Fatalf("ReadAt err=%v, want %v", err, wantErr)
				}
			})
		}
	}
}

func TestEncryptingReaderAtConcurrentReads(t *testing.T) {
	key := testKey(0x53)
	params := testParams(t, 53)
	plain := deterministicBytes(t, 53, format.ChunkSize*4+99)
	want := encryptToBytes(t, plain, key, params)
	reader := newReaderAtFixture(t, plain, key, params)

	var wait sync.WaitGroup
	failures := make(chan string, 16)
	for worker := range 16 {
		wait.Go(func() {
			off := int64(worker * 7919 % (len(want) - 3*format.ChunkSize))
			got := make([]byte, 2*format.ChunkSize+13)
			n, err := reader.ReadAt(got, off)
			if err != nil || !bytes.Equal(got[:n], want[off:off+int64(n)]) {
				failures <- fmt.Sprintf("worker %d off %d n %d err %v", worker, off, n, err)
			}
		})
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

type shortPlaintext struct {
	data []byte
	err  error
}

func (s shortPlaintext) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, s.data[min(off, int64(len(s.data))):])
	if n < len(p) {
		return n, s.err
	}
	return n, nil
}

func TestEncryptingReaderAtRejectsBadInputs(t *testing.T) {
	params := testParams(t, 54)
	key := testKey(0x54)
	if _, err := NewEncryptingReaderAt(nil, 0, key, params.Salt, params.NoncePrefix); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil source error = %v, want ErrMissingDependency", err)
	}
	if _, err := NewEncryptingReaderAt(bytes.NewReader(nil), 0, key[:31], params.Salt, params.NoncePrefix); !errors.Is(err, ErrInvalidMasterKey) {
		t.Fatalf("short key error = %v, want ErrInvalidMasterKey", err)
	}
	if _, err := NewEncryptingReaderAt(bytes.NewReader(nil), -1, key, params.Salt, params.NoncePrefix); !errors.Is(err, format.ErrNegativePlaintextSize) {
		t.Fatalf("negative size error = %v, want ErrNegativePlaintextSize", err)
	}

	short, err := NewEncryptingReaderAt(shortPlaintext{data: make([]byte, 10), err: io.EOF}, 100, key, params.Salt, params.NoncePrefix)
	if err != nil {
		t.Fatalf("NewEncryptingReaderAt: %v", err)
	}
	if _, err := short.ReadAt(make([]byte, 8), format.HeaderLen); !errors.Is(err, ErrPlaintextSizeMismatch) {
		t.Fatalf("short plaintext error = %v, want ErrPlaintextSizeMismatch", err)
	}
	broken := errors.New("disk failure")
	failing, err := NewEncryptingReaderAt(shortPlaintext{data: make([]byte, 10), err: broken}, 100, key, params.Salt, params.NoncePrefix)
	if err != nil {
		t.Fatalf("NewEncryptingReaderAt: %v", err)
	}
	if _, err := failing.ReadAt(make([]byte, 8), format.HeaderLen); !errors.Is(err, broken) {
		t.Fatalf("source failure error = %v, want wrapped source error", err)
	}

	reader := newReaderAtFixture(t, []byte("abc"), key, params)
	if _, err := reader.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close again: %v", err)
	}
	if _, err := reader.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrReaderClosed) {
		t.Fatalf("read after Close error = %v, want ErrReaderClosed", err)
	}
}

func TestEncryptingReaderAtCloseClearsSubkey(t *testing.T) {
	params := testParams(t, 55)
	reader := newReaderAtFixture(t, []byte("abc"), testKey(0x55), params)
	retained := reader.subkey
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, b := range retained {
		if b != 0 {
			t.Fatal("Close did not zero the retained subkey")
		}
	}
}
