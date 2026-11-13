package sqlitedb

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func createPrivateFile(fsys fsx.FS, path string) error {
	tmp, err := newPrivateTemp(fsys, path)
	if err != nil {
		return err
	}
	if err := fsys.Rename(tmp, path); err != nil {
		_ = fsys.Remove(tmp)
		return fmt.Errorf("sqlitedb: create database file: %w", err)
	}
	return nil
}

func newPrivateTemp(fsys fsx.FS, path string) (string, error) {
	file, err := fsys.CreateTemp(filepath.Dir(path), filepath.Base(path)+".new-*")
	if err != nil {
		return "", fmt.Errorf("sqlitedb: create database file: %w", err)
	}
	name := file.Name()
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		_ = fsys.Remove(name)
		return "", fmt.Errorf("sqlitedb: restrict database file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = fsys.Remove(name)
		return "", fmt.Errorf("sqlitedb: create database file: %w", err)
	}
	return name, nil
}
