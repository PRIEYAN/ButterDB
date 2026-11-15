package blockcache

import (
	"container/heap"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

var (
	errEntryTooLarge = errors.New("blockcache: block exceeds cache budget")
	errCacheFull     = errors.New("blockcache: cache is full")
	errCacheDisabled = errors.New("blockcache: cache is disabled")
)

type Files interface {
	fsx.FS
	Open(name string) (io.ReadCloser, error)
}

type entry struct {
	name    string
	size    int64
	written time.Time
}

type counters struct {
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	fillBytes atomic.Int64
}

type store struct {
	files      Files
	dir        string
	maxBytes   int64
	maxEntries int
	logger     *slog.Logger
	stats      *counters

	mu              sync.Mutex
	entries         map[string]*list.Element
	lru             *list.List
	used            int64
	pending         int64
	overflowBytes   int64
	overflowEntries int
	inited          bool
	active          int
	cond            *sync.Cond
}

func newStore(files Files, dir string, maxBytes int64, maxEntries int, logger *slog.Logger, stats *counters) *store {
	s := &store{
		files:      files,
		dir:        filepath.Clean(dir),
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
		logger:     logger,
		stats:      stats,
		entries:    make(map[string]*list.Element),
		lru:        list.New(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *store) entryLimit() int {
	if s.maxEntries < 1 {
		return 1
	}
	return s.maxEntries
}

func (s *store) path(name string) string {
	return filepath.Join(s.dir, name)
}

func (s *store) init() {
	s.mu.Lock()
	s.ensureInitLocked()
	s.mu.Unlock()
}

func (s *store) read(key blockKey, dst []byte) bool {
	name := key.relativeName()
	s.mu.Lock()
	s.ensureInitLocked()
	elem, known := s.entries[name]
	if !known {
		s.mu.Unlock()
		return false
	}
	if elem.Value.(entry).size != int64(len(dst)) {
		s.dropLocked(name, elem)
		s.mu.Unlock()
		return false
	}
	s.lru.MoveToFront(elem)
	s.mu.Unlock()

	if !s.readFile(name, dst) {
		s.mu.Lock()
		s.dropLocked(name, elem)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *store) readFile(name string, dst []byte) bool {
	file, err := s.files.Open(s.path(name))
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	if _, err := io.ReadFull(file, dst); err != nil {
		return false
	}
	var extra [1]byte
	n, err := file.Read(extra[:])
	return n == 0 && errors.Is(err, io.EOF)
}

func (s *store) dropLocked(name string, elem *list.Element) {
	if s.entries[name] != elem {
		return
	}
	s.removeLocked(name)
}

func (s *store) write(key blockKey, data []byte) error {
	size := int64(len(data))
	if size == 0 {
		return nil
	}
	name := key.relativeName()
	s.mu.Lock()
	if s.maxBytes <= 0 {
		s.mu.Unlock()
		return errCacheDisabled
	}
	if size > s.maxBytes {
		s.mu.Unlock()
		return errEntryTooLarge
	}
	s.ensureInitLocked()
	if elem, present := s.entries[name]; present && elem.Value.(entry).size == size {
		s.lru.MoveToFront(elem)
		s.mu.Unlock()
		return nil
	}
	if !s.reserveLocked(size) {
		s.mu.Unlock()
		return errCacheFull
	}
	s.active++
	s.mu.Unlock()

	reserved := true
	defer func() {
		s.mu.Lock()
		if reserved {
			s.pending -= size
		}
		s.active--
		s.cond.Broadcast()
		s.mu.Unlock()
	}()

	if err := s.publish(name, data); err != nil {
		return err
	}

	s.mu.Lock()
	s.pending -= size
	reserved = false
	s.forgetLocked(name)
	s.entries[name] = s.lru.PushFront(entry{name: name, size: size})
	s.used += size
	s.stats.fillBytes.Add(size)
	s.evictLocked()
	s.mu.Unlock()
	return nil
}

func (s *store) publish(name string, data []byte) error {
	path := s.path(name)
	entryDir := filepath.Dir(path)
	temp, err := s.createTemp(entryDir)
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if err := writeSynced(temp, data); err != nil {
		_ = s.files.Remove(tempName)
		return err
	}
	if err := s.files.Rename(tempName, path); err != nil {
		_ = s.files.Remove(tempName)
		return fmt.Errorf("blockcache: publish block: %w", err)
	}
	if err := s.files.SyncDir(entryDir); err != nil {
		_ = s.files.Remove(path)
		return fmt.Errorf("blockcache: sync block directory: %w", err)
	}
	return nil
}

func writeSynced(temp fsx.File, data []byte) error {
	if err := temp.Chmod(fileMode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("blockcache: chmod block: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("blockcache: write block: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("blockcache: sync block: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("blockcache: close block: %w", err)
	}
	return nil
}

// A concurrent eviction may prune the shard directory between MkdirAll and CreateTemp.
func (s *store) createTemp(entryDir string) (fsx.File, error) {
	var lastErr error
	for range 3 {
		if err := s.makeShardDirs(entryDir); err != nil {
			return nil, err
		}
		temp, err := s.files.CreateTemp(entryDir, tempFilePrefix+"*")
		if err == nil {
			return temp, nil
		}
		lastErr = err
		if !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	return nil, fmt.Errorf("blockcache: create temp block: %w", lastErr)
}

func (s *store) makeShardDirs(entryDir string) error {
	if err := s.files.MkdirAll(entryDir, dirMode); err != nil {
		return fmt.Errorf("blockcache: create shard directory: %w", err)
	}
	for _, dir := range []string{s.dir, filepath.Dir(entryDir), entryDir} {
		_ = s.files.Chmod(dir, dirMode)
	}
	return nil
}

func (s *store) usage() (used, maxBytes int64, entries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureInitLocked()
	return s.used + s.overflowBytes, s.maxBytes, len(s.entries) + s.overflowEntries
}

func (s *store) clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.active > 0 {
		s.cond.Wait()
	}
	err := s.removeTree(s.dir, false)
	s.entries = make(map[string]*list.Element)
	s.lru.Init()
	s.used = 0
	s.pending = 0
	s.overflowBytes = 0
	s.overflowEntries = 0
	s.inited = err == nil
	if err != nil {
		return fmt.Errorf("blockcache: clear: %w", err)
	}
	return nil
}

func (s *store) removeTree(dir string, removeSelf bool) error {
	children, err := s.files.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, child := range children {
		path := filepath.Join(dir, child.Name())
		if child.IsDir() {
			if err := s.removeTree(path, true); err != nil {
				return err
			}
			continue
		}
		if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if !removeSelf {
		return nil
	}
	if err := s.files.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *store) ensureInitLocked() {
	if s.inited {
		return
	}
	s.inited = true
	found := make(entryHeap, 0, min(s.entryLimit(), 1024))
	var foundBytes int64
	s.scanLocked("", &found, &foundBytes)
	slices.SortFunc(found, func(a, b entry) int {
		if order := b.written.Compare(a.written); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	for _, e := range found {
		s.entries[e.name] = s.lru.PushBack(e)
		s.used += e.size
	}
	s.evictLocked()
}

func (s *store) scanLocked(rel string, found *entryHeap, foundBytes *int64) {
	dir := s.dir
	if rel != "" {
		dir = s.path(rel)
	}
	children, err := s.files.ReadDir(dir)
	if err != nil {
		return
	}
	for _, child := range children {
		childRel := filepath.Join(rel, child.Name())
		if child.IsDir() {
			_ = s.files.Chmod(s.path(childRel), dirMode)
			s.scanLocked(childRel, found, foundBytes)
			continue
		}
		if child.Type()&fs.ModeSymlink != 0 {
			continue
		}
		s.scanFileLocked(childRel, child, found, foundBytes)
	}
}

func (s *store) scanFileLocked(rel string, child fs.DirEntry, found *entryHeap, foundBytes *int64) {
	name := child.Name()
	path := s.path(rel)
	if strings.HasPrefix(name, tempFilePrefix) {
		_ = s.files.Remove(path)
		return
	}
	if !strings.HasSuffix(name, blockFileSuffix) {
		return
	}
	info, err := child.Info()
	if err != nil {
		return
	}
	if info.Size() <= 0 {
		_ = s.files.Remove(path)
		return
	}
	candidate := entry{name: rel, size: info.Size(), written: info.ModTime()}
	heap.Push(found, candidate)
	*foundBytes += candidate.size
	for len(*found) > 1 && (len(*found) > s.entryLimit() || *foundBytes > s.maxBytes) {
		oldest := heap.Pop(found).(entry)
		*foundBytes -= oldest.size
		if err := s.files.Remove(s.path(oldest.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.overflowBytes += oldest.size
			s.overflowEntries++
			continue
		}
		s.stats.evictions.Add(1)
	}
}

func (s *store) evictLocked() bool {
	if s.overflowEntries > 0 {
		return false
	}
	for s.used+s.pending > s.maxBytes || len(s.entries) > s.entryLimit() {
		removed := false
		for elem := s.lru.Back(); elem != nil; elem = elem.Prev() {
			if s.removeLocked(elem.Value.(entry).name) {
				s.stats.evictions.Add(1)
				removed = true
				break
			}
		}
		if !removed {
			return false
		}
	}
	return true
}

func (s *store) reserveLocked(size int64) bool {
	s.pending += size
	if s.evictLocked() {
		return true
	}
	s.pending -= size
	return false
}

func (s *store) removeLocked(name string) bool {
	if _, ok := s.entries[name]; !ok {
		return true
	}
	path := s.path(name)
	if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logger.Debug("blockcache: remove block failed", "error", err)
		return false
	}
	s.forgetLocked(name)
	s.pruneShardsLocked(filepath.Dir(path))
	return true
}

func (s *store) pruneShardsLocked(shard string) {
	for range 2 {
		if shard == s.dir || shard == "." || shard == string(filepath.Separator) {
			return
		}
		if err := s.files.Remove(shard); err != nil {
			return
		}
		shard = filepath.Dir(shard)
	}
}

func (s *store) forgetLocked(name string) {
	elem, ok := s.entries[name]
	if !ok {
		return
	}
	s.used -= elem.Value.(entry).size
	s.lru.Remove(elem)
	delete(s.entries, name)
}

type entryHeap []entry

func (h entryHeap) Len() int { return len(h) }

func (h entryHeap) Less(i, j int) bool {
	if h[i].written.Equal(h[j].written) {
		return h[i].name > h[j].name
	}
	return h[i].written.Before(h[j].written)
}

func (h entryHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(value any) { *h = append(*h, value.(entry)) }

func (h *entryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}
