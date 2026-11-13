package secretfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

type recorder struct {
	events []string
}

func (r *recorder) add(event string) {
	r.events = append(r.events, event)
}

type tracingFS struct {
	fsx.FS
	rec           *recorder
	modeAtWrite   fs.FileMode
	renameErr     error
	syncDirErr    error
	writeErr      error
	shortWrite    bool
	tempSyncErr   error
	removeErr     error
	chmodTempErr  error
	createTempErr error
}

type tracingFile struct {
	fsx.File
	owner *tracingFS
}

func (f *tracingFile) Chmod(mode fs.FileMode) error {
	f.owner.rec.add("chmod-temp")
	if f.owner.chmodTempErr != nil {
		return f.owner.chmodTempErr
	}
	return f.File.Chmod(mode)
}

func (f *tracingFile) Write(p []byte) (int, error) {
	f.owner.rec.add("write")
	if info, err := os.Stat(f.Name()); err == nil {
		f.owner.modeAtWrite = info.Mode().Perm()
	}
	if f.owner.writeErr != nil {
		return 0, f.owner.writeErr
	}
	if f.owner.shortWrite {
		n, err := f.File.Write(p[:len(p)/2])
		if err != nil {
			return n, err
		}
		return n, nil
	}
	return f.File.Write(p)
}

func (f *tracingFile) Sync() error {
	f.owner.rec.add("sync-temp")
	if f.owner.tempSyncErr != nil {
		return f.owner.tempSyncErr
	}
	return f.File.Sync()
}

func (t *tracingFS) CreateTemp(dir, pattern string) (fsx.File, error) {
	t.rec.add("create-temp")
	if t.createTempErr != nil {
		return nil, t.createTempErr
	}
	file, err := t.FS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(file.Name(), 0o644); err != nil {
		return nil, err
	}
	return &tracingFile{File: file, owner: t}, nil
}

func (t *tracingFS) Rename(oldpath, newpath string) error {
	t.rec.add("rename")
	if t.renameErr != nil {
		return t.renameErr
	}
	return t.FS.Rename(oldpath, newpath)
}

func (t *tracingFS) SyncDir(path string) error {
	t.rec.add("sync-dir")
	if t.syncDirErr != nil {
		return t.syncDirErr
	}
	return t.FS.SyncDir(path)
}

func (t *tracingFS) Remove(name string) error {
	t.rec.add("remove-temp")
	if t.removeErr != nil {
		return t.removeErr
	}
	return t.FS.Remove(name)
}

func newTracing() *tracingFS {
	return &tracingFS{FS: fsx.OS{}, rec: &recorder{}}
}

func newWriter(t *testing.T, fsys fsx.FS) *Writer {
	t.Helper()
	w, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func TestNewRejectsNilFilesystem(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilFilesystem) {
		t.Fatalf("New(nil) err = %v, want ErrNilFilesystem", err)
	}
}

func TestSecretFileModeBeforeWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.FileMode does not represent ACL permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	fsys := newTracing()

	if err := newWriter(t, fsys).Write(path, []byte("secret")); err != nil {
		t.Fatal(err)
	}

	if fsys.modeAtWrite != 0o600 {
		t.Fatalf("temp mode at first write = %04o, want 0600 (temp was created 0644)", fsys.modeAtWrite)
	}
	want := []string{"create-temp", "chmod-temp", "write", "sync-temp", "rename", "sync-dir"}
	if !slices.Equal(fsys.rec.events, want) {
		t.Fatalf("event order = %v, want %v", fsys.rec.events, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("final file info=%v err=%v", info, err)
	}
	if names := dirEntries(t, dir); !slices.Equal(names, []string{"session.json"}) {
		t.Fatalf("directory entries = %v", names)
	}
}

func TestWritePrivateFileReplacesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte("old credentials"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	if err := newWriter(t, fsx.OS{}).Write(path, []byte("new credentials")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if got, want := string(contents), "new credentials"; got != want {
		t.Fatalf("replacement = %q, want %q", got, want)
	}
}

func TestWritePrivateFileUsesRestrictiveModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.FileMode does not represent ACL permissions")
	}

	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "credentials.json")
	if err := newWriter(t, fsx.OS{}).Write(path, []byte("secret")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat private file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != fileMode {
		t.Fatalf("file mode = %04o, want %04o", got, fileMode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat private directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != dirMode {
		t.Fatalf("directory mode = %04o, want %04o", got, dirMode)
	}
}

func TestWritePrivateFileRenameFailurePreservesOldFileAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	const oldContents = "working credentials"
	if err := os.WriteFile(path, []byte(oldContents), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	injectedErr := errors.New("injected rename failure")
	fsys := newTracing()
	fsys.renameErr = injectedErr

	err := newWriter(t, fsys).Write(path, []byte("replacement credentials"))
	if !errors.Is(err, injectedErr) {
		t.Fatalf("Write error = %v, want injected rename failure", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read original after failure: %v", readErr)
	}
	if got := string(contents); got != oldContents {
		t.Fatalf("original after failure = %q, want %q", got, oldContents)
	}
	if names := dirEntries(t, dir); !slices.Equal(names, []string{"credentials.json"}) {
		t.Fatalf("directory entries after failure = %v, want only credentials.json", names)
	}
}

func TestWriteFailureLeavesNoTargetAndNoTemp(t *testing.T) {
	boom := errors.New("injected failure")
	for _, tc := range []struct {
		name    string
		inject  func(f *tracingFS)
		wantErr error
	}{
		{"create temp fails", func(f *tracingFS) { f.createTempErr = boom }, boom},
		{"chmod fails before any byte", func(f *tracingFS) { f.chmodTempErr = boom }, boom},
		{"write fails", func(f *tracingFS) { f.writeErr = boom }, boom},
		{"short write", func(f *tracingFS) { f.shortWrite = true }, io.ErrShortWrite},
		{"sync fails", func(f *tracingFS) { f.tempSyncErr = boom }, boom},
		{"rename fails", func(f *tracingFS) { f.renameErr = boom }, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.json")
			fsys := newTracing()
			tc.inject(fsys)

			err := newWriter(t, fsys).Write(path, []byte("secret bytes"))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Write err = %v, want %v", err, tc.wantErr)
			}
			if names := dirEntries(t, dir); len(names) != 0 {
				t.Fatalf("directory entries after failure = %v, want none", names)
			}
		})
	}
}

func TestWriteChmodFailureWritesNoBytes(t *testing.T) {
	fsys := newTracing()
	fsys.chmodTempErr = errors.New("injected chmod failure")
	path := filepath.Join(t.TempDir(), "session.json")

	if err := newWriter(t, fsys).Write(path, []byte("secret")); err == nil {
		t.Fatal("Write succeeded with a failing chmod")
	}
	if slices.Contains(fsys.rec.events, "write") {
		t.Fatalf("bytes were written before the mode was secured: %v", fsys.rec.events)
	}
}

func TestWriteReportsDirectorySyncFailure(t *testing.T) {
	boom := errors.New("injected dir sync failure")
	fsys := newTracing()
	fsys.syncDirErr = boom
	path := filepath.Join(t.TempDir(), "session.json")

	if err := newWriter(t, fsys).Write(path, []byte("secret")); !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want directory sync failure", err)
	}
}

func TestWriteJoinsTempRemovalFailure(t *testing.T) {
	renameFailure := errors.New("injected rename failure")
	removeFailure := errors.New("injected remove failure")
	fsys := newTracing()
	fsys.renameErr = renameFailure
	fsys.removeErr = removeFailure
	path := filepath.Join(t.TempDir(), "session.json")

	err := newWriter(t, fsys).Write(path, []byte("secret"))
	if !errors.Is(err, renameFailure) || !errors.Is(err, removeFailure) {
		t.Fatalf("Write err = %v, want both rename and remove failures", err)
	}
}

func TestWriteErrorsNameNoSecretBytes(t *testing.T) {
	fsys := newTracing()
	fsys.renameErr = errors.New("injected rename failure")
	path := filepath.Join(t.TempDir(), "session.json")

	err := newWriter(t, fsys).Write(path, []byte("hunter2-secret"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); strings.Contains(got, "hunter2") {
		t.Fatalf("error text leaks secret bytes: %q", got)
	}
}
