package secretfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

var ErrNilFilesystem = errors.New("secretfile: filesystem is required")

type Writer struct {
	fsys fsx.FS
}

func New(fsys fsx.FS) (*Writer, error) {
	if fsys == nil {
		return nil, ErrNilFilesystem
	}
	return &Writer{fsys: fsys}, nil
}

func (w *Writer) Write(path string, data []byte) error {
	dirPath := filepath.Dir(path)
	if err := w.fsys.MkdirAll(dirPath, dirMode); err != nil {
		return fmt.Errorf("secretfile: create private directory: %w", err)
	}
	if err := w.fsys.Chmod(dirPath, dirMode); err != nil {
		return fmt.Errorf("secretfile: secure private directory: %w", err)
	}
	if err := w.publish(dirPath, path, data); err != nil {
		return err
	}
	if err := w.fsys.SyncDir(dirPath); err != nil {
		return fmt.Errorf("secretfile: sync private file directory: %w", err)
	}
	return nil
}

func (w *Writer) publish(dirPath, path string, data []byte) (retErr error) {
	temp, err := w.fsys.CreateTemp(dirPath, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("secretfile: create private temp file: %w", err)
	}
	tempPath := temp.Name()
	tempOpen := true
	removeTemp := true
	defer func() {
		if tempOpen {
			if err := temp.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("secretfile: close private temp file: %w", err))
			}
		}
		if removeTemp {
			if err := w.fsys.Remove(tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("secretfile: remove private temp file: %w", err))
			}
		}
	}()

	// The mode must be 0600 before the first secret byte reaches the operating system.
	if err := temp.Chmod(fileMode); err != nil {
		return fmt.Errorf("secretfile: secure private temp file: %w", err)
	}
	written, err := temp.Write(data)
	if err != nil {
		return fmt.Errorf("secretfile: write private temp file: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("secretfile: write private temp file: %w", io.ErrShortWrite)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("secretfile: sync private temp file: %w", err)
	}
	tempOpen = false
	if err := temp.Close(); err != nil {
		return fmt.Errorf("secretfile: close private temp file: %w", err)
	}
	if err := w.fsys.Rename(tempPath, path); err != nil {
		return fmt.Errorf("secretfile: replace private file: %w", err)
	}
	removeTemp = false
	return nil
}
