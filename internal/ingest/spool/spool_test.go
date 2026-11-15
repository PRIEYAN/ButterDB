package spool

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func TestSpoolHashesMatch(t *testing.T) {
	t.Parallel()

	big := make([]byte, 3*copyBufferSize+17)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		payload []byte
		reader  func(io.Reader) io.Reader
		decl    int64
	}{
		{"empty", nil, identityReader, 0},
		{"small declared", []byte("hello"), identityReader, 5},
		{"multi chunk declared", big, identityReader, int64(len(big))},
		{"undeclared", big, identityReader, -1},
		{"one byte reads", []byte("one byte at a time"), iotest.OneByteReader, 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
			got, err := store.Write(context.Background(), "job", tc.reader(bytes.NewReader(tc.payload)), tc.decl, 0)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if got.Size != int64(len(tc.payload)) {
				t.Fatalf("size = %d, want %d", got.Size, len(tc.payload))
			}
			if got.MD5 != md5.Sum(tc.payload) {
				t.Fatalf("md5 = %x", got.MD5)
			}
			if got.SHA256 != sha256.Sum256(tc.payload) {
				t.Fatalf("sha256 = %x", got.SHA256)
			}
		})
	}
}

func identityReader(r io.Reader) io.Reader { return r }

func (r *recordingFS) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

type recordingFS struct {
	fsx.OS
	mu     sync.Mutex
	events []string
}

type recordingFile struct {
	fsx.File
	owner *recordingFS
}

func (f recordingFile) Sync() error {
	f.owner.record("sync-file")
	return f.File.Sync()
}

func (f recordingFile) Close() error {
	f.owner.record("close-file")
	return f.File.Close()
}

func (r *recordingFS) CreateTemp(dir, pattern string) (fsx.File, error) {
	file, err := r.OS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	r.record("create-temp")
	return recordingFile{File: file, owner: r}, nil
}

func (r *recordingFS) Rename(oldpath, newpath string) error {
	r.record("rename")
	return r.OS.Rename(oldpath, newpath)
}

func (r *recordingFS) SyncDir(path string) error {
	r.record("sync-dir")
	return r.OS.SyncDir(path)
}

func TestFsyncBeforeRename(t *testing.T) {
	t.Parallel()

	recorder := &recordingFS{}
	store, err := New(filepath.Join(t.TempDir(), "spool"), recorder, clock.NewFake(time.Unix(1700000000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), "job", strings.NewReader("payload"), 7, 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := []string{"create-temp", "sync-file", "close-file", "rename", "sync-dir"}
	if strings.Join(recorder.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", recorder.events, want)
	}
}

func TestDeclaredLengthEnforced(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		body      string
		declared  int64
		maxStored int64
		wantErr   error
	}{
		{"exact", "12345", 5, 5, nil},
		{"short body", "123", 5, 0, ErrLengthMismatch},
		{"long body", "1234567", 5, 0, ErrLengthMismatch},
		{"declared above cap", "12345", 5, 4, errs.ErrObjectTooLarge},
		{"undeclared above cap", "123456", -1, 4, errs.ErrObjectTooLarge},
		{"undeclared within cap", "1234", -1, 4, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
			_, err := store.Write(context.Background(), "job", strings.NewReader(tc.body), tc.declared, tc.maxStored)
			if !errors.Is(err, tc.wantErr) && (tc.wantErr != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			entries, readErr := os.ReadDir(store.dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if (tc.wantErr == nil) != (len(entries) == 1) {
				t.Fatalf("entries = %v for err %v", entries, err)
			}
		})
	}
}

type countingReader struct {
	remaining int
	served    int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining, 1024)
	r.remaining -= n
	r.served += n
	return n, nil
}

func TestDeclaredLengthStopsReadingEarly(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
	source := &countingReader{remaining: 1 << 20}
	_, err := store.Write(context.Background(), "job", source, 2048, 0)
	if !errors.Is(err, ErrLengthMismatch) {
		t.Fatalf("err = %v, want ErrLengthMismatch", err)
	}
	if source.served > 2048+1024 {
		t.Fatalf("read %d bytes after a 2048 byte declaration", source.served)
	}
}

type failingRenameFS struct {
	fsx.OS
}

func (failingRenameFS) Rename(string, string) error { return errors.New("rename refused") }

type failingSyncFS struct {
	fsx.OS
}

func (failingSyncFS) CreateTemp(dir, pattern string) (fsx.File, error) {
	file, err := fsx.OS{}.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return failingSyncFile{File: file}, nil
}

type failingSyncFile struct {
	fsx.File
}

func (failingSyncFile) Sync() error { return errors.New("sync refused") }

func TestTempRemovedOnError(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		fs   fsx.FS
		ctx  context.Context
		body io.Reader
		decl int64
	}{
		{"rename fails", failingRenameFS{}, context.Background(), strings.NewReader("payload"), 7},
		{"sync fails", failingSyncFS{}, context.Background(), strings.NewReader("payload"), 7},
		{"reader fails", fsx.OS{}, context.Background(), io.MultiReader(strings.NewReader("part"), iotest.ErrReader(errSourceFailure)), -1},
		{"length mismatch", fsx.OS{}, context.Background(), strings.NewReader("payload"), 3},
		{"context canceled", fsx.OS{}, canceled, strings.NewReader("payload"), 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := NewWithBudget(filepath.Join(t.TempDir(), "spool"), tc.fs, clock.NewFake(time.Unix(1700000000, 0)), Budget{MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Write(tc.ctx, "job", tc.body, tc.decl, 0); err == nil {
				t.Fatal("write succeeded, want error")
			}
			entries, readErr := os.ReadDir(store.dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("temp files left: entries=%v err=%v", entries, readErr)
			}
			if store.UsedBytes() != 0 {
				t.Fatalf("retained %d bytes", store.UsedBytes())
			}
		})
	}
}

func TestBytesUnchanged(t *testing.T) {
	t.Parallel()

	jpegWithExif := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10}, []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00")...)
	jpegWithExif = append(jpegWithExif, 0xFF, 0xD9)
	cases := []struct {
		name    string
		payload []byte
	}{
		{"jpeg with exif block", jpegWithExif},
		{"crlf text", []byte("line one\r\nline two\r\n")},
		{"nul and high bytes", []byte{0, 1, 2, 0xFF, 0xFE, 0, 0}},
		{"random binary", bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}, copyBufferSize/3)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
			file, err := store.Write(context.Background(), "job", bytes.NewReader(tc.payload), int64(len(tc.payload)), 0)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			onDisk, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(onDisk, tc.payload) {
				t.Fatal("spooled bytes differ from the uploaded bytes")
			}
			reader, err := store.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := reader.Seek(2, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			tail, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(tail, tc.payload[2:]) {
				t.Fatalf("seek and read: err=%v equal=%v", err, bytes.Equal(tail, tc.payload[2:]))
			}
		})
	}
}

func TestConcurrentWritesOfOneJobAreRejected(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
	blocking := newBlockingReader()
	firstDone := make(chan error, 1)
	go func() {
		_, err := store.Write(context.Background(), "job", blocking, -1, 0)
		firstDone <- err
	}()
	blocking.waitUntilRead(t)
	if _, err := store.Write(context.Background(), "job", strings.NewReader("x"), 1, 0); !errors.Is(err, ErrJobFileExists) {
		t.Fatalf("concurrent write error = %v, want ErrJobFileExists", err)
	}
	blocking.unblock()
	if err := <-firstDone; err != nil {
		t.Fatalf("first write: %v", err)
	}
}

func TestRemoveDeletesSealedCompanion(t *testing.T) {
	t.Parallel()

	store := newTestSpool(t, filepath.Join(t.TempDir(), "spool"), Budget{})
	if _, err := store.Write(context.Background(), "job", strings.NewReader("abc"), 3, 0); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(store.dir, "job.sealed")
	if err := os.WriteFile(sealed, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("job"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if entries, err := os.ReadDir(store.dir); err != nil || len(entries) != 0 {
		t.Fatalf("entries after remove = %v err=%v", entries, err)
	}
}
