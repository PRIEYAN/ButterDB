package spool

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const (
	copyBufferSize = 256 * 1024
	dirMode        = fs.FileMode(0o700)
	fileMode       = fs.FileMode(0o600)
	plainSuffix    = ".plain"
	sealedSuffix   = ".sealed"
	tempMarker     = ".tmp-"
	maxJobIDLength = 128
)

type Budget struct {
	MaxBytes      int64
	MaxConcurrent int
}

type Spool struct {
	dir    string
	fs     fsx.FS
	budget Budget
	slots  chan struct{}

	mu       sync.Mutex
	used     int64
	tracked  map[string]int64
	inflight map[string]struct{}
}

func New(dir string, fsys fsx.FS, clk clock.Clock) (*Spool, error) {
	return NewWithBudget(dir, fsys, clk, Budget{})
}

func NewWithBudget(dir string, fsys fsx.FS, clk clock.Clock, budget Budget) (*Spool, error) {
	if dir == "" {
		return nil, fmt.Errorf("spool: %w: dir", ErrInvalidRequest)
	}
	if fsys == nil {
		return nil, fmt.Errorf("spool: %w: fs", errs.ErrMissingDependency)
	}
	if clk == nil {
		return nil, fmt.Errorf("spool: %w: clock", errs.ErrMissingDependency)
	}
	if budget.MaxBytes < 0 || budget.MaxConcurrent < 0 {
		return nil, fmt.Errorf("spool: %w: budget", ErrInvalidRequest)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("spool: resolve dir: %w", err)
	}
	s := &Spool{
		dir:      absDir,
		fs:       fsys,
		budget:   budget,
		tracked:  make(map[string]int64),
		inflight: make(map[string]struct{}),
	}
	if budget.MaxConcurrent > 0 {
		s.slots = make(chan struct{}, budget.MaxConcurrent)
	}
	if err := s.prepareDir(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Spool) prepareDir() error {
	if err := s.fs.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("spool: create dir: %w", err)
	}
	if err := s.fs.Chmod(s.dir, dirMode); err != nil {
		return fmt.Errorf("spool: chmod dir: %w", err)
	}
	entries, err := s.fs.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("spool: scan dir: %w", err)
	}
	for _, entry := range entries {
		if err := s.adoptEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) adoptEntry(entry fs.DirEntry) error {
	if entry.IsDir() {
		return nil
	}
	name := entry.Name()
	if strings.Contains(name, tempMarker) {
		if err := s.fs.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("spool: remove leftover temp file: %w", err)
		}
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("spool: inspect existing file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	s.tracked[name] = info.Size()
	s.used += info.Size()
	return nil
}

func (s *Spool) Write(ctx context.Context, jobID string, body io.Reader, declared int64, maxStored int64) (File, error) {
	if ctx == nil || body == nil || !validJobID(jobID) || declared < -1 || maxStored < 0 {
		return File{}, fmt.Errorf("spool: write: %w", ErrInvalidRequest)
	}
	if maxStored > 0 && declared > maxStored {
		return File{}, fmt.Errorf("spool: write: declared length exceeds cap: %w", errs.ErrObjectTooLarge)
	}
	if err := s.acquireSlot(ctx); err != nil {
		return File{}, err
	}
	defer s.releaseSlot()
	if err := s.markInflight(jobID); err != nil {
		return File{}, err
	}
	defer s.clearInflight(jobID)
	finalPath := s.plainPath(jobID)
	if _, err := s.fs.Stat(finalPath); err == nil {
		return File{}, fmt.Errorf("spool: write: %w", ErrJobFileExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return File{}, fmt.Errorf("spool: stat job file: %w", err)
	}
	return s.spoolToFile(ctx, jobID, body, declared, maxStored)
}

func (s *Spool) spoolToFile(ctx context.Context, jobID string, body io.Reader, declared, maxStored int64) (File, error) {
	reserved := int64(0)
	if declared > 0 {
		if err := s.reserve(declared); err != nil {
			return File{}, err
		}
		reserved = declared
	}
	temp, err := s.fs.CreateTemp(s.dir, jobID+plainSuffix+tempMarker+"*")
	if err != nil {
		s.release(reserved)
		return File{}, fmt.Errorf("spool: create temp file: %w", err)
	}
	result, reserved, copyErr := s.fillAndPublish(ctx, temp, jobID, body, declared, maxStored, reserved)
	if copyErr != nil {
		s.discardTemp(temp.Name(), reserved)
		return File{}, copyErr
	}
	s.track(filepath.Base(result.Path), result.Size)
	return result, nil
}

func (s *Spool) fillAndPublish(ctx context.Context, temp fsx.File, jobID string, body io.Reader, declared, maxStored, reserved int64) (File, int64, error) {
	result, reserved, err := s.copyBody(ctx, temp, body, declared, maxStored, reserved)
	closeErr := temp.Close()
	if err != nil {
		return File{}, reserved, err
	}
	if closeErr != nil {
		return File{}, reserved, fmt.Errorf("spool: close temp file: %w", closeErr)
	}
	finalPath := s.plainPath(jobID)
	if err := s.fs.Rename(temp.Name(), finalPath); err != nil {
		return File{}, reserved, fmt.Errorf("spool: publish file: %w", err)
	}
	result.Path = finalPath
	if err := s.fs.SyncDir(s.dir); err != nil {
		s.discardTemp(finalPath, reserved)
		return File{}, 0, fmt.Errorf("spool: sync dir: %w", err)
	}
	return result, reserved, nil
}

func (s *Spool) copyBody(ctx context.Context, temp fsx.File, body io.Reader, declared, maxStored, reserved int64) (File, int64, error) {
	if err := temp.Chmod(fileMode); err != nil {
		return File{}, reserved, fmt.Errorf("spool: chmod temp file: %w", err)
	}
	source := body
	if declared >= 0 {
		source = &exactLengthReader{ctx: ctx, source: body, remaining: declared}
	}
	limit := int64(math.MaxInt64)
	if maxStored > 0 {
		limit = maxStored
	}
	md5Hash := md5.New()
	sha256Hash := sha256.New()
	sink := io.MultiWriter(temp, md5Hash, sha256Hash)
	buffer := make([]byte, copyBufferSize)
	var size int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return File{}, reserved, fmt.Errorf("spool: write: %w", err)
		}
		readCount, readErr := source.Read(buffer)
		if readCount > 0 {
			emptyReads = 0
			var err error
			if reserved, err = s.admitChunk(size, int64(readCount), limit, reserved); err != nil {
				return File{}, reserved, err
			}
			if err := writeAll(sink, buffer[:readCount]); err != nil {
				return File{}, reserved, fmt.Errorf("spool: write temp file: %w", err)
			}
			size += int64(readCount)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return File{}, reserved, classifyReadError(readErr)
		}
		if readCount == 0 {
			emptyReads++
			if emptyReads >= maxEmptyReads {
				return File{}, reserved, fmt.Errorf("spool: read body: %w", io.ErrNoProgress)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return File{}, reserved, fmt.Errorf("spool: write: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return File{}, reserved, fmt.Errorf("spool: sync temp file: %w", err)
	}
	result := File{Size: size}
	copy(result.MD5[:], md5Hash.Sum(nil))
	copy(result.SHA256[:], sha256Hash.Sum(nil))
	return result, reserved, nil
}

func (s *Spool) admitChunk(size, chunk, limit, reserved int64) (int64, error) {
	if chunk > limit || size > limit-chunk {
		return reserved, fmt.Errorf("spool: write: body exceeds cap: %w", errs.ErrObjectTooLarge)
	}
	need := size + chunk
	if need <= reserved {
		return reserved, nil
	}
	if err := s.reserve(need - reserved); err != nil {
		return reserved, err
	}
	return need, nil
}

func classifyReadError(err error) error {
	if errors.Is(err, ErrLengthMismatch) {
		return fmt.Errorf("spool: read body: %w", ErrLengthMismatch)
	}
	return fmt.Errorf("spool: read body: %w", err)
}

func (s *Spool) discardTemp(path string, reserved int64) {
	err := s.fs.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		s.release(reserved)
		return
	}
	s.track(filepath.Base(path), reserved)
}

func (s *Spool) Open(f File) (io.ReadSeekCloser, error) {
	name, err := s.ownedName(f.Path)
	if err != nil || f.Size < 0 {
		return nil, fmt.Errorf("spool: open: %w", ErrInvalidRequest)
	}
	file, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("spool: open: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("spool: open: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("spool: inspect file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		_ = file.Close()
		return nil, fmt.Errorf("spool: open: %w", ErrFileCorrupt)
	}
	return file, nil
}

func (s *Spool) Remove(jobID string) error {
	if !validJobID(jobID) {
		return fmt.Errorf("spool: remove: %w", ErrInvalidRequest)
	}
	for _, suffix := range []string{plainSuffix, sealedSuffix} {
		if err := s.removeNamed(jobID + suffix); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) removeNamed(name string) error {
	path := filepath.Join(s.dir, name)
	info, err := s.fs.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.forget(name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("spool: stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("spool: remove: %w", ErrInvalidRequest)
	}
	if err := s.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("spool: remove file: %w", err)
	}
	s.forget(name)
	return nil
}

func (s *Spool) ownedName(path string) (string, error) {
	if path == "" || filepath.Dir(filepath.Clean(path)) != s.dir {
		return "", ErrInvalidRequest
	}
	name := filepath.Base(path)
	jobID, ok := strings.CutSuffix(name, plainSuffix)
	if !ok || !validJobID(jobID) {
		return "", ErrInvalidRequest
	}
	return name, nil
}

func (s *Spool) plainPath(jobID string) string {
	return filepath.Join(s.dir, jobID+plainSuffix)
}

func (s *Spool) acquireSlot(ctx context.Context) error {
	if s.slots == nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("spool: wait for slot: %w", err)
		}
		return nil
	}
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("spool: wait for slot: %w", ctx.Err())
	}
}

func (s *Spool) releaseSlot() {
	if s.slots != nil {
		<-s.slots
	}
}

func (s *Spool) markInflight(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inflight[jobID]; busy {
		return fmt.Errorf("spool: write: %w", ErrJobFileExists)
	}
	s.inflight[jobID] = struct{}{}
	return nil
}

func (s *Spool) clearInflight(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, jobID)
}

func (s *Spool) reserve(size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if size < 0 || (s.budget.MaxBytes > 0 && s.used > s.budget.MaxBytes-size) {
		return ErrBudgetExhausted
	}
	s.used += size
	return nil
}

func (s *Spool) release(size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used = max(s.used-size, 0)
}

func (s *Spool) track(name string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracked[name] = size
}

func (s *Spool) forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	size, ok := s.tracked[name]
	if !ok {
		return
	}
	delete(s.tracked, name)
	s.used = max(s.used-size, 0)
}

func (s *Spool) UsedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

func validJobID(jobID string) bool {
	if jobID == "" || len(jobID) > maxJobIDLength {
		return false
	}
	for _, r := range jobID {
		if !isJobIDRune(r) {
			return false
		}
	}
	return true
}

func isJobIDRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
