package processlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

const (
	fileMode       os.FileMode = 0o600
	guardSuffix                = ".guard"
	currentVersion             = 1
	unknownRole                = "unknown"
)

var (
	ErrAlreadyRunning = errors.New("butterdbd already running")
	ErrNilClock       = errors.New("processlock: clock is required")
)

type Info struct {
	Role        string `json:"role"`
	PID         int    `json:"pid"`
	StartedAt   int64  `json:"started_at"`
	LockVersion int    `json:"lock_version,omitzero"`
}

type Lock struct {
	mu   sync.Mutex
	file *os.File
	info Info
}

func Acquire(path, role string, clk clock.Clock) (*Lock, error) {
	if clk == nil {
		return nil, ErrNilClock
	}
	if role == "" {
		role = unknownRole
	}
	f, err := createLockFile(path)
	if err != nil {
		return nil, err
	}
	info := Info{
		Role:        role,
		PID:         os.Getpid(),
		StartedAt:   clk.Now().Unix(),
		LockVersion: currentVersion,
	}
	if err := writeInfo(path, info); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{file: f, info: info}, nil
}

func createLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path+guardSuffix, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return nil, fmt.Errorf("processlock: acquire: %w", err)
	}
	if err := tryLockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, ownerError(path)
		}
		return nil, fmt.Errorf("processlock: acquire: %w", err)
	}
	if legacyOwnerActive(path) {
		_ = f.Close()
		return nil, ownerError(path)
	}
	return f, nil
}

func legacyOwnerActive(path string) bool {
	info, err := readInfo(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	if info.LockVersion == currentVersion {
		return false
	}
	if info.LockVersion != 0 {
		return true
	}
	return processRunning(info.PID)
}

func ownerError(path string) error {
	info, err := readInfo(path)
	if err == nil && info.Role != "" {
		return fmt.Errorf("processlock: %w as %s (pid %d)", ErrAlreadyRunning, info.Role, info.PID)
	}
	return fmt.Errorf("processlock: %w", ErrAlreadyRunning)
}

func writeInfo(path string, info Info) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".butterdbd-lock-*")
	if err != nil {
		return fmt.Errorf("processlock: metadata: %w", err)
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	writeErr := json.NewEncoder(f).Encode(info)
	if writeErr == nil {
		writeErr = f.Chmod(fileMode)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("processlock: metadata: %w", err)
	}
	// A missing metadata path is reserved with O_EXCL so a record written by an older owner is never replaced unchecked.
	reserved, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	created := err == nil
	if created {
		if err := reserved.Close(); err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("processlock: metadata: %w", err)
		}
	} else if errors.Is(err, os.ErrExist) {
		if legacyOwnerActive(path) {
			return ownerError(path)
		}
	} else {
		return fmt.Errorf("processlock: metadata: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		if created {
			_ = os.Remove(path)
		}
		return fmt.Errorf("processlock: metadata: %w", err)
	}
	return nil
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Lock) Info() Info {
	if l == nil {
		return Info{}
	}
	return l.info
}

func Read(path string) (Info, error) {
	return readInfo(path)
}

func readInfo(path string) (Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func ReadActive(path string) (Info, bool, error) {
	f, err := createLockFile(path)
	info, readErr := readInfo(path)
	if errors.Is(err, ErrAlreadyRunning) {
		if readErr != nil {
			info = Info{Role: unknownRole}
		}
		return info, true, nil
	}
	if err != nil {
		return Info{}, false, err
	}
	if err := f.Close(); err != nil {
		return Info{}, false, fmt.Errorf("processlock: release probe: %w", err)
	}
	return info, false, nil
}

func ProcessRunning(pid int) bool {
	return processRunning(pid)
}
