package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func newRooted(t *testing.T) (*Rooted, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, r.Dir()
}

func TestNewRootRejectsBadInput(t *testing.T) {
	if _, err := NewRoot(""); !errors.Is(err, ErrEmptyRoot) {
		t.Fatalf("empty err = %v", err)
	}
	if _, err := NewRoot(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestRootedRoundTrip(t *testing.T) {
	var fsys FS
	r, dir := newRooted(t)
	fsys = r
	sub := filepath.Join(dir, "a", "b")

	if err := fsys.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Chmod(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := fsys.Stat(sub)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("stat dir info=%v err=%v", info, err)
	}

	temp, err := fsys.CreateTemp(sub, "scratch-*.part")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(temp.Name()) != sub {
		t.Fatalf("temp %q not in %q", temp.Name(), sub)
	}
	if base := filepath.Base(temp.Name()); len(base) <= len("scratch-.part") {
		t.Fatalf("temp name %q has no unique part", base)
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

	target := filepath.Join(sub, "final")
	if err := fsys.Rename(temp.Name(), target); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir(sub); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hello" {
		t.Fatalf("content=%q err=%v", got, err)
	}
	entries, err := fsys.ReadDir(sub)
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

func TestRootedCreateTempUniqueNames(t *testing.T) {
	r, dir := newRooted(t)
	seen := map[string]bool{}
	for range 5 {
		f, err := r.CreateTemp(dir, "t-*")
		if err != nil {
			t.Fatal(err)
		}
		if seen[f.Name()] {
			t.Fatalf("duplicate temp name %q", f.Name())
		}
		seen[f.Name()] = true
		_ = f.Close()
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 5 {
		t.Fatalf("names = %v", names)
	}
}

func TestRootedRejectsEscape(t *testing.T) {
	r, dir := newRooted(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inside"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{
		"dotdot relative":  filepath.Join("..", filepath.Base(outside), "secret"),
		"dotdot absolute":  filepath.Join(dir, "sub", "..", "..", filepath.Base(outside), "secret"),
		"absolute outside": filepath.Join(outside, "secret"),
		"absolute parent":  filepath.Dir(dir),
		"bare dotdot":      "..",
		"empty":            "",
	}
	for name, path := range paths {
		calls := map[string]func() error{
			"MkdirAll": func() error { return r.MkdirAll(path, 0o700) },
			"Chmod":    func() error { return r.Chmod(path, 0o600) },
			"Remove":   func() error { return r.Remove(path) },
			"Stat":     func() error { _, err := r.Stat(path); return err },
			"ReadDir":  func() error { _, err := r.ReadDir(path); return err },
			"SyncDir":  func() error { return r.SyncDir(path) },
			"CreateTemp": func() error {
				f, err := r.CreateTemp(path, "x-*")
				if f != nil {
					t.Error("CreateTemp returned a file for an escaping dir")
				}
				return err
			},
			"Rename from": func() error { return r.Rename(path, filepath.Join(dir, "moved")) },
			"Rename to":   func() error { return r.Rename(filepath.Join(dir, "inside"), path) },
		}
		for call, fn := range calls {
			t.Run(name+" "+call, func(t *testing.T) {
				if err := fn(); !errors.Is(err, ErrOutsideRoot) {
					t.Fatalf("err = %v, want ErrOutsideRoot", err)
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); err != nil {
		t.Fatalf("outside file disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "inside")); err != nil {
		t.Fatalf("inside file disturbed: %v", err)
	}
}

func TestRootedRejectsSeparatorInPattern(t *testing.T) {
	r, dir := newRooted(t)
	if _, err := r.CreateTemp(dir, "a/b-*"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v", err)
	}
}

func TestRootedRefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	r, dir := newRooted(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "filelink")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"stat through dir link", func() error { _, err := r.Stat(filepath.Join(link, "secret")); return err }},
		{"stat file link", func() error { _, err := r.Stat(filepath.Join(dir, "filelink")); return err }},
		{"read dir link", func() error { _, err := r.ReadDir(link); return err }},
		{"sync dir link", func() error { return r.SyncDir(link) }},
		{"mkdir through link", func() error { return r.MkdirAll(filepath.Join(link, "new"), 0o700) }},
		{"chmod through link", func() error { return r.Chmod(filepath.Join(link, "secret"), 0o644) }},
		{"remove through link", func() error { return r.Remove(filepath.Join(link, "secret")) }},
		{"create temp through link", func() error {
			f, err := r.CreateTemp(link, "x-*")
			if f != nil {
				t.Error("CreateTemp returned a file through a symlink")
			}
			return err
		}},
		{"rename into link", func() error {
			if err := os.WriteFile(filepath.Join(dir, "src"), []byte("y"), 0o600); err != nil {
				t.Fatal(err)
			}
			return r.Rename(filepath.Join(dir, "src"), filepath.Join(link, "dst"))
		}},
		{"rename out of link", func() error { return r.Rename(filepath.Join(link, "secret"), filepath.Join(dir, "got")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "dst")); err == nil {
		t.Fatal("rename escaped through symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("mkdir escaped through symlink")
	}
	if info, err := os.Stat(filepath.Join(outside, "secret")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("outside secret info=%v err=%v", info, err)
	}
}

func TestRootedErrors(t *testing.T) {
	r, dir := newRooted(t)
	missing := filepath.Join(dir, "missing")
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"stat missing", func() error { _, err := r.Stat(missing); return err }},
		{"read missing dir", func() error { _, err := r.ReadDir(missing); return err }},
		{"remove missing", func() error { return r.Remove(missing) }},
		{"sync missing dir", func() error { return r.SyncDir(missing) }},
		{"chmod missing", func() error { return r.Chmod(missing, 0o600) }},
		{"rename missing", func() error { return r.Rename(missing, filepath.Join(dir, "x")) }},
		{"create temp in missing dir", func() error {
			file, err := r.CreateTemp(missing, "x-*")
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
