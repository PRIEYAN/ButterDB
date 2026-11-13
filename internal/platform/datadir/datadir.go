package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const (
	dirMode        fs.FileMode = 0o700
	groupOtherBits fs.FileMode = 0o077
	lockFileName               = "butterdbd.lock"
)

var (
	ErrNilFilesystem = errors.New("datadir: filesystem is required")
	ErrEmptyRoot     = errors.New("datadir: root path is empty")
	ErrNotDirectory  = errors.New("datadir: path is not a directory")
	ErrLooseMode     = errors.New("datadir: directory mode grants group or other access")
	ErrForeignOwner  = errors.New("datadir: directory is not owned by the process user")
)

func ownedTempPrefixes() []string {
	return []string{"butterdb-spool-", "butterdb-part-"}
}

type Dir struct {
	fsys fsx.FS
	root string
}

func Open(fsys fsx.FS, root string) (*Dir, error) {
	if fsys == nil {
		return nil, ErrNilFilesystem
	}
	if root == "" {
		return nil, ErrEmptyRoot
	}
	root = filepath.Clean(root)
	if err := ensurePrivateDir(fsys, root); err != nil {
		return nil, err
	}
	return &Dir{fsys: fsys, root: root}, nil
}

func (d *Dir) Path() string {
	return d.root
}

func (d *Dir) Join(elem ...string) string {
	return filepath.Join(append([]string{d.root}, elem...)...)
}

func (d *Dir) LockPath() string {
	return d.Join(lockFileName)
}

func (d *Dir) Subdir(name string) (string, error) {
	path := d.Join(name)
	if err := ensurePrivateDir(d.fsys, path); err != nil {
		return "", err
	}
	return path, nil
}

func (d *Dir) CreateCacheTemp(pattern string) (fsx.File, error) {
	return d.fsys.CreateTemp(d.root, pattern)
}

// A scratch file that cannot be removed is left for the next start; it must not stop the server from starting.
func (d *Dir) CleanupCacheTemps() error {
	entries, err := d.fsys.ReadDir(d.root)
	if err != nil {
		return fmt.Errorf("datadir: read temp directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isOwnedTemp(entry.Name()) {
			continue
		}
		_ = d.fsys.Remove(filepath.Join(d.root, entry.Name()))
	}
	return nil
}

func isOwnedTemp(name string) bool {
	for _, prefix := range ownedTempPrefixes() {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func ensurePrivateDir(fsys fsx.FS, path string) error {
	info, err := fsys.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := fsys.MkdirAll(path, dirMode); err != nil {
			return fmt.Errorf("datadir: create %q: %w", path, err)
		}
		info, err = fsys.Stat(path)
	}
	if err != nil {
		return fmt.Errorf("datadir: stat %q: %w", path, err)
	}
	return checkPrivate(path, info)
}

func checkPrivate(path string, info fs.FileInfo) error {
	if !info.IsDir() {
		return fmt.Errorf("%w: %q", ErrNotDirectory, path)
	}
	if info.Mode().Perm()&groupOtherBits != 0 {
		return fmt.Errorf("%w: %q has mode %04o, want 0700", ErrLooseMode, path, info.Mode().Perm())
	}
	if !ownedByProcessUser(info) {
		return fmt.Errorf("%w: %q", ErrForeignOwner, path)
	}
	return nil
}
