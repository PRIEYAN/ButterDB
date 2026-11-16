package vm

import (
	"io"
	"os"
)

type ReadFile interface {
	io.ReadSeeker
	io.Closer
}

type FileSystem interface {
	Open(path string) (ReadFile, error)
	Create(path string) (io.WriteCloser, error)
	MkdirAll(dir string) error
	Rename(from, to string) error
	Remove(path string) error
	Exists(path string) bool
}

type osFileSystem struct{}

func (osFileSystem) Open(path string) (ReadFile, error) {
	return os.Open(path)
}

func (osFileSystem) Create(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

func (osFileSystem) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func (osFileSystem) Rename(from, to string) error {
	return os.Rename(from, to)
}

func (osFileSystem) Remove(path string) error {
	return os.Remove(path)
}

func (osFileSystem) Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func fileSize(f ReadFile) (int64, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return size, nil
}
