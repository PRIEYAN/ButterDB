package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeFiles struct {
	metas    map[string]fileMeta
	links    map[string]string
	contents map[string][]byte
}

func (f fakeFiles) Lstat(path string) (fileMeta, error) {
	meta, ok := f.metas[path]
	if !ok {
		return fileMeta{}, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
	}
	return meta, nil
}

func (f fakeFiles) Readlink(path string) (string, error) {
	target, ok := f.links[path]
	if !ok {
		return "", &fs.PathError{Op: "readlink", Path: path, Err: fs.ErrInvalid}
	}
	return target, nil
}

func (f fakeFiles) ReadFile(path string) ([]byte, error) {
	data, ok := f.contents[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return data, nil
}

func (f fakeFiles) LookPath(name string) (string, error) {
	return "", &fs.PathError{Op: "lookpath", Path: name, Err: fs.ErrNotExist}
}

type testApp struct {
	app    *application
	stdout *syncBuffer
	stderr *syncBuffer
}

func newTestApp(environ []string) testApp {
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	app := newApplication(stdout, stderr, environ, buildInfo{Version: "1.2.3", Commit: "abc1234"})
	return testApp{app: app, stdout: stdout, stderr: stderr}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeKeyFile(t *testing.T, dir string, mode fs.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "server.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("k", serverKeyBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runInBackground(app *application, ctx context.Context, args []string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- app.run(ctx, args) }()
	return done
}
