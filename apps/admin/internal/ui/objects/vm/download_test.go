package vm

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type probeReader struct {
	src   io.Reader
	probe func()
	once  bool
}

func (p *probeReader) Read(b []byte) (int, error) {
	if !p.once {
		p.once = true
		p.probe()
	}
	return p.src.Read(b)
}

func (p *probeReader) Close() error { return nil }

func md5Quoted(data []byte) string {
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func streamOf(data []byte, etag string, probe func()) func() adminv1.ObjectStream {
	return func() adminv1.ObjectStream {
		var body io.ReadCloser = io.NopCloser(bytes.NewReader(data))
		if probe != nil {
			body = &probeReader{src: bytes.NewReader(data), probe: probe}
		}
		return adminv1.ObjectStream{Body: body, Status: http.StatusOK, ContentLength: int64(len(data)), Meta: adminv1.ObjectMeta{ETag: etag}}
	}
}

func TestDownloadWritesPartThenRenames(t *testing.T) {
	data := []byte("hello object")
	dir := t.TempDir()
	target := filepath.Join(dir, "cat.txt")
	part := PartFilePath(target)
	if filepath.Base(part) != ".cat.txt.butterdb-part" {
		t.Fatalf("part file name = %q", filepath.Base(part))
	}
	c := newFakeClient()
	partSeenMidStream := false
	targetAbsentMidStream := false
	c.streams["a/cat.txt"] = streamOf(data, md5Quoted(data), func() {
		_, partErr := os.Stat(part)
		_, targetErr := os.Stat(target)
		partSeenMidStream = partErr == nil
		targetAbsentMidStream = os.IsNotExist(targetErr)
	})
	v, _ := newTestVM(t, c)
	v.Download(context.Background(), "photos", []DownloadRequest{{Key: "a/cat.txt", TargetPath: target}}, Prompts{})
	if !partSeenMidStream || !targetAbsentMidStream {
		t.Fatalf("part present=%v target absent=%v during the stream", partSeenMidStream, targetAbsentMidStream)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("target = %q, %v", got, err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("part file remains: %v", err)
	}
	if item, _ := transferByKey(v.Transfers().Get(), "a/cat.txt"); item.State != TransferDone || item.Sent != int64(len(data)) {
		t.Fatalf("item = %+v", item)
	}
}

func TestDownloadVerifiesMD5(t *testing.T) {
	data := []byte("payload bytes")
	cases := []struct {
		name      string
		etag      string
		wantState TransferState
	}{
		{"matching md5", md5Quoted(data), TransferDone},
		{"wrong md5", md5Quoted([]byte("different")), TransferFailed},
		{"multipart etag skipped", `"` + hex.EncodeToString(make([]byte, 16)) + `-3"`, TransferDone},
		{"non hex etag skipped", `"not-an-md5"`, TransferDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "f.bin")
			c := newFakeClient()
			c.streams["f.bin"] = streamOf(data, tc.etag, nil)
			v, _ := newTestVM(t, c)
			v.Download(context.Background(), "b", []DownloadRequest{{Key: "f.bin", TargetPath: target}}, Prompts{})
			item, _ := transferByKey(v.Transfers().Get(), "f.bin")
			if item.State != tc.wantState {
				t.Fatalf("state = %s, want %s (%+v)", item.State, tc.wantState, item.Problem)
			}
			_, targetErr := os.Stat(target)
			if (targetErr == nil) != (tc.wantState == TransferDone) {
				t.Fatalf("target presence = %v", targetErr)
			}
			if _, err := os.Stat(PartFilePath(target)); !os.IsNotExist(err) {
				t.Fatalf("part file left behind: %v", err)
			}
		})
	}
}

func TestDownloadShortBodyFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.bin")
	c := newFakeClient()
	c.streams["f.bin"] = func() adminv1.ObjectStream {
		return adminv1.ObjectStream{Body: io.NopCloser(bytes.NewReader([]byte("abc"))), Status: 200, ContentLength: 10}
	}
	v, _ := newTestVM(t, c)
	v.Download(context.Background(), "b", []DownloadRequest{{Key: "f.bin", TargetPath: target}}, Prompts{})
	item, _ := transferByKey(v.Transfers().Get(), "f.bin")
	if item.State != TransferFailed {
		t.Fatalf("state = %s", item.State)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("truncated download was renamed into place: %v", err)
	}
}

func TestDownloadExistingTargetChoices(t *testing.T) {
	data := []byte("new")
	cases := []struct {
		name       string
		choice     ConflictChoice
		wantTarget string
		wantFile   string
	}{
		{"replace", ConflictReplace, "doc.txt", "new"},
		{"keep both", ConflictKeepBoth, "doc (1).txt", "new"},
		{"skip", ConflictSkip, "doc.txt", "old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "doc.txt")
			if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			c := newFakeClient()
			c.streams["doc.txt"] = streamOf(data, md5Quoted(data), nil)
			v, _ := newTestVM(t, c)
			v.Download(context.Background(), "b", []DownloadRequest{{Key: "doc.txt", TargetPath: target}}, Prompts{Conflict: func(context.Context, string) ConflictChoice { return tc.choice }})
			got, err := os.ReadFile(filepath.Join(dir, tc.wantTarget))
			if err != nil || string(got) != tc.wantFile {
				t.Fatalf("%s = %q, %v", tc.wantTarget, got, err)
			}
		})
	}
}

func TestDownloadKeySanitised(t *testing.T) {
	cases := []struct {
		name      string
		folder    string
		prefix    string
		key       string
		separator rune
		want      string
	}{
		{"relative to prefix", "/dl", "photos/", "photos/2026/cat.jpg", '/', "/dl/2026/cat.jpg"},
		{"dot dot segments replaced", "/dl", "", "../../etc/passwd", '/', "/dl/_/_/etc/passwd"},
		{"single dot replaced", "/dl", "", "a/./b", '/', "/dl/a/_/b"},
		{"empty segments dropped", "/dl", "", "a//b", '/', "/dl/a/b"},
		{"windows separator in segment", "/dl", "", `a\b/c`, '\\', "/dl/_/c"},
		{"unix backslash is a plain character", "/dl", "", `a\b`, '/', `/dl/a\b`},
		{"key equal to prefix keeps last segment", "/dl", "dir/", "dir/", '/', "/dl/dir"},
		{"only dots", "/dl", "", "..", '/', "/dl/_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filepath.ToSlash(SafeLocalPath(tc.folder, tc.prefix, tc.key, tc.separator))
			if got != tc.want {
				t.Fatalf("SafeLocalPath = %q, want %q", got, tc.want)
			}
			if rel, err := filepath.Rel(tc.folder, SafeLocalPath(tc.folder, tc.prefix, tc.key, tc.separator)); err != nil || rel == ".." || len(rel) > 2 && rel[:3] == "../" {
				t.Fatalf("path escapes the folder: %q (%v)", rel, err)
			}
		})
	}
}

func TestDefaultFileName(t *testing.T) {
	cases := map[string]string{"a/b/cat.jpg": "cat.jpg", "plain": "plain", "dir/": "download", "x/..": "_"}
	for key, want := range cases {
		if got := DefaultFileName(key); got != want {
			t.Errorf("DefaultFileName(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestDownloadFailureKeepsNoPartFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x")
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	v.Download(context.Background(), "b", []DownloadRequest{{Key: "missing", TargetPath: target}}, Prompts{})
	item, _ := transferByKey(v.Transfers().Get(), "missing")
	if item.State != TransferFailed || item.Problem == nil || item.Problem.Code != adminv1.CodeObjectNotFound {
		t.Fatalf("item = %+v", item)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
}
