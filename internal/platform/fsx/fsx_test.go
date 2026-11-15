package fsx

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOSRoundTrip(t *testing.T) {
	var fsys FS = OS{}
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")

	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := fsys.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("stat dir info=%v err=%v", info, err)
	}

	temp, err := fsys.CreateTemp(dir, "scratch-*")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(temp.Name()) != dir {
		t.Fatalf("temp %q not in %q", temp.Name(), dir)
	}
	if _, err := temp.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := temp.Chmod(0o600); err != nil {
		t.Fatal(err)
	}
	if err := temp.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "final")
	if err := fsys.Rename(temp.Name(), target); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := fsys.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "final" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if err := fsys.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after remove err=%v", err)
	}
}

func TestOSErrors(t *testing.T) {
	var fsys FS = OS{}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"stat missing", func() error { _, err := fsys.Stat(missing); return err }},
		{"read missing dir", func() error { _, err := fsys.ReadDir(missing); return err }},
		{"remove missing", func() error { return fsys.Remove(missing) }},
		{"sync missing dir", func() error { return fsys.SyncDir(missing) }},
		{"create temp in missing dir", func() error {
			file, err := fsys.CreateTemp(missing, "x-*")
			if file != nil {
				t.Error("CreateTemp returned a non-nil file with an error")
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("err = %v, want not exist", err)
			}
		})
	}
}

func TestOSOpen(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "data")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		path    string
		want    string
		wantErr error
	}{
		{"round trip", target, "payload", nil},
		{"missing file", filepath.Join(root, "missing"), "", os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, err := OS{}.Open(tc.path)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || rc != nil {
					t.Fatalf("rc=%v err=%v, want %v", rc, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("read %q, want %q", got, tc.want)
			}
		})
	}
}
