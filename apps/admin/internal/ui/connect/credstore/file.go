package credstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	tokenFileExtension = ".token"
	tokenFileMode      = 0o600
	tokenDirMode       = 0o700
	tokenDirName       = "tokens"
	appDirName         = "butterdb-admin"
)

var ErrInvalidServerID = errors.New("server id is not a safe file name")

type File struct {
	dir string
	mu  sync.Mutex
}

func TokenDirIn(configDir string) string {
	return filepath.Join(configDir, appDirName, tokenDirName)
}

func NewFile(dir string) (*File, error) {
	if dir == "" {
		return nil, errors.New("credstore: file store needs a directory")
	}
	return &File{dir: dir}, nil
}

func (f *File) Get(serverID string) (string, error) {
	path, err := f.pathFor(serverID)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("credstore: file get: %w", model.ErrTokenNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("credstore: file get: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (f *File) Set(serverID, token string) error {
	path, err := f.pathFor(serverID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ensureDir(f.dir); err != nil {
		return fmt.Errorf("credstore: file set: %w", err)
	}
	if err := writeAtomic(f.dir, path, []byte(token)); err != nil {
		return fmt.Errorf("credstore: file set: %w", err)
	}
	return nil
}

func (f *File) Delete(serverID string) error {
	path, err := f.pathFor(serverID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("credstore: file delete: %w", model.ErrTokenNotFound)
	}
	if err != nil {
		return fmt.Errorf("credstore: file delete: %w", err)
	}
	return nil
}

func (f *File) Kind() model.TokenStorage {
	return model.TokenFile
}

func (f *File) pathFor(serverID string) (string, error) {
	if !safeServerID(serverID) {
		return "", fmt.Errorf("credstore: %w", ErrInvalidServerID)
	}
	return filepath.Join(f.dir, serverID+tokenFileExtension), nil
}

func safeServerID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		isAlnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !isAlnum && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, tokenDirMode); err != nil {
		return err
	}
	return os.Chmod(dir, tokenDirMode)
}

func writeAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := writeTemp(tmp, data); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func writeTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(tokenFileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	return tmp.Close()
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
