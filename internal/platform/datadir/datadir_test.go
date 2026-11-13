package datadir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func privateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDataDirRefusesLooseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not honour unix permission bits")
	}
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		wantErr error
	}{
		{"0700 is accepted", 0o700, nil},
		{"0500 is accepted", 0o500, nil},
		{"0755 refuses start", 0o755, ErrLooseMode},
		{"0750 refuses start", 0o750, ErrLooseMode},
		{"0705 refuses start", 0o705, ErrLooseMode},
		{"0770 refuses start", 0o770, ErrLooseMode},
		{"0777 refuses start", 0o777, ErrLooseMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateRoot(t)
			if err := os.Chmod(root, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

			dir, err := Open(fsx.OS{}, root)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Open err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if dir != nil {
					t.Fatal("Open returned a Dir together with an error")
				}
				info, statErr := os.Stat(root)
				if statErr != nil || info.Mode().Perm() != tc.mode {
					t.Fatalf("Open changed the mode: info=%v err=%v", info, statErr)
				}
			}
		})
	}
}

func TestOpenCreatesMissingDirectoryPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "data")
	dir, err := Open(fsx.OS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir.Path())
	if err != nil || !info.IsDir() {
		t.Fatalf("stat created dir info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("created mode = %04o, want no group or other access", info.Mode().Perm())
	}
}

func TestOpenRejects(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		fsys    fsx.FS
		root    string
		wantErr error
	}{
		{"nil filesystem", nil, "x", ErrNilFilesystem},
		{"empty root", fsx.OS{}, "", ErrEmptyRoot},
		{"root is a file", fsx.OS{}, notDir, ErrNotDirectory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Open(tc.fsys, tc.root); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Open err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPaths(t *testing.T) {
	root := privateRoot(t)
	dir, err := Open(fsx.OS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Path(); got != root {
		t.Fatalf("Path = %q, want %q", got, root)
	}
	if got, want := dir.LockPath(), filepath.Join(root, "butterdbd.lock"); got != want {
		t.Fatalf("LockPath = %q, want %q", got, want)
	}
	if got, want := dir.Join("secrets", "x.json"), filepath.Join(root, "secrets", "x.json"); got != want {
		t.Fatalf("Join = %q, want %q", got, want)
	}
}

func TestSubdirIsPrivate(t *testing.T) {
	dir, err := Open(fsx.OS{}, privateRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	path, err := dir.Subdir("spool")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("subdir info=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("subdir mode = %04o, want 0700", info.Mode().Perm())
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := dir.Subdir("spool"); !errors.Is(err, ErrLooseMode) {
			t.Fatalf("loose subdir err = %v, want ErrLooseMode", err)
		}
	}
}

func TestCreateCacheTempUsesPrivateCacheOverride(t *testing.T) {
	base := privateRoot(t)
	dir, err := Open(fsx.OS{}, base)
	if err != nil {
		t.Fatal(err)
	}
	file, err := dir.CreateCacheTemp("butterdb-spool-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if got := filepath.Dir(file.Name()); got != base {
		t.Fatalf("temp parent = %q, want %q", got, base)
	}
	info, err := os.Stat(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("temp mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestCleanupCacheTempsRemovesOnlyOwnedScratchFiles(t *testing.T) {
	base := privateRoot(t)
	dir, err := Open(fsx.OS{}, base)
	if err != nil {
		t.Fatal(err)
	}
	owned := []string{"butterdb-spool-orphan", "butterdb-part-orphan"}
	for _, name := range owned {
		if err := os.WriteFile(filepath.Join(base, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"keep.cache", "tdrive-upload-orphan"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("unrelated"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := dir.CleanupCacheTemps(); err != nil {
		t.Fatal(err)
	}
	for _, name := range owned {
		if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
			t.Fatalf("owned temp %q remains: %v", name, err)
		}
	}
	for _, name := range []string{"keep.cache", "tdrive-upload-orphan"} {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Fatalf("unrelated file %q removed: %v", name, err)
		}
	}
}

type removeFailingFS struct {
	fsx.FS
	stuck string
}

func (f removeFailingFS) Remove(name string) error {
	if strings.HasSuffix(name, f.stuck) {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrPermission}
	}
	return f.FS.Remove(name)
}

func TestCleanupCacheTempsKeepsGoingWhenOneCannotBeRemoved(t *testing.T) {
	base := privateRoot(t)
	fsys := removeFailingFS{FS: fsx.OS{}, stuck: "butterdb-part-stuck"}
	dir, err := Open(fsys, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "butterdb-part-dir", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"butterdb-part-stuck", "butterdb-spool-a", "butterdb-spool-z"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := dir.CleanupCacheTemps(); err != nil {
		t.Fatalf("a scratch file nobody can delete stopped the cleanup: %v", err)
	}
	for _, name := range []string{"butterdb-spool-a", "butterdb-spool-z"} {
		if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
			t.Fatalf("the deletable orphan %q remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "butterdb-part-stuck")); err != nil {
		t.Fatalf("the stuck file should be left in place: %v", err)
	}
}

func TestCleanupCacheTempsReportsUnreadableDirectory(t *testing.T) {
	base := privateRoot(t)
	dir, err := Open(fsx.OS{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	if err := dir.CleanupCacheTemps(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not exist", err)
	}
}
