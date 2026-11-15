package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
)

const maxTempAttempts = 10000

var (
	ErrEmptyRoot   = errors.New("fsx: root directory is empty")
	ErrOutsideRoot = errors.New("fsx: path is outside the root")
)

type Rooted struct {
	dir     string
	root    *os.Root
	counter atomic.Uint64
}

type rootedFile struct {
	*os.File
	name string
}

func (f *rootedFile) Name() string {
	return f.name
}

func NewRoot(dir string) (*Rooted, error) {
	if dir == "" {
		return nil, ErrEmptyRoot
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("fsx: resolve root: %w", err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("fsx: open root: %w", err)
	}
	return &Rooted{dir: abs, root: root}, nil
}

func (r *Rooted) Dir() string {
	return r.dir
}

func (r *Rooted) Close() error {
	return r.root.Close()
}

func (r *Rooted) rel(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("fsx: empty path: %w", ErrOutsideRoot)
	}
	rel := filepath.Clean(name)
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(r.dir, rel)
		if err != nil {
			return "", fmt.Errorf("fsx: %w", ErrOutsideRoot)
		}
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fsx: %w", ErrOutsideRoot)
	}
	return rel, nil
}

func (r *Rooted) MkdirAll(path string, perm fs.FileMode) error {
	rel, err := r.rel(path)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	return r.root.MkdirAll(rel, perm)
}

func (r *Rooted) Chmod(name string, mode fs.FileMode) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.root.Chmod(rel, mode)
}

func (r *Rooted) CreateTemp(dir, pattern string) (File, error) {
	if strings.ContainsRune(pattern, filepath.Separator) {
		return nil, fmt.Errorf("fsx: temp pattern contains a path separator: %w", ErrOutsideRoot)
	}
	relDir, err := r.rel(dir)
	if err != nil {
		return nil, err
	}
	prefix, suffix := pattern, ""
	if i := strings.LastIndex(pattern, "*"); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	for range maxTempAttempts {
		base := prefix + strconv.FormatUint(r.counter.Add(1), 10) + suffix
		file, err := r.root.OpenFile(filepath.Join(relDir, base), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &rootedFile{File: file, name: filepath.Join(r.dir, relDir, base)}, nil
	}
	return nil, fmt.Errorf("fsx: no free temp name: %w", fs.ErrExist)
}

func (r *Rooted) Open(name string) (io.ReadCloser, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	file, err := r.root.Open(rel)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (r *Rooted) Rename(oldpath, newpath string) error {
	oldRel, err := r.rel(oldpath)
	if err != nil {
		return err
	}
	newRel, err := r.rel(newpath)
	if err != nil {
		return err
	}
	return r.root.Rename(oldRel, newRel)
}

func (r *Rooted) Remove(name string) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.root.Remove(rel)
}

func (r *Rooted) Stat(name string) (fs.FileInfo, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	return r.root.Stat(rel)
}

func (r *Rooted) ReadDir(name string) (entries []fs.DirEntry, retErr error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	dir, err := r.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := dir.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	entries, err = dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *Rooted) SyncDir(path string) (retErr error) {
	rel, err := r.rel(path)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := r.root.Open(rel)
	if err != nil {
		return err
	}
	defer func() {
		if err := dir.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	return dir.Sync()
}
