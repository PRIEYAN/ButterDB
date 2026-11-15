package fsx

import (
	"io"
	"io/fs"
	"os"
	"runtime"
)

type File interface {
	io.Writer
	io.Closer
	Name() string
	Chmod(mode fs.FileMode) error
	Sync() error
}

type FS interface {
	MkdirAll(path string, perm fs.FileMode) error
	Chmod(name string, mode fs.FileMode) error
	CreateTemp(dir, pattern string) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	SyncDir(path string) error
}

type OS struct{}

func (OS) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OS) Chmod(name string, mode fs.FileMode) error {
	return os.Chmod(name, mode)
}

func (OS) CreateTemp(dir, pattern string) (File, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (OS) Open(name string) (io.ReadCloser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (OS) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (OS) Remove(name string) error {
	return os.Remove(name)
}

func (OS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (OS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

// os.File.Sync has no portable directory equivalent on Windows.
func (OS) SyncDir(path string) (retErr error) {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
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
