package mediaqueue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/mediaservice"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

const (
	DefaultRemoveDelay = 5 * time.Minute
	dirPrefix          = "butterdb-queue-"
	sweepAge           = time.Hour
)

var (
	ErrDownload = errors.New("mediaqueue: download failed")
	ErrPlayback = errors.New("mediaqueue: playback failed")
)

type Player interface {
	OpenNativePlaylist(bucket string, keys []string, opts mediaservice.OpenOptions) (mediaservice.Result, error)
	AppendLocalFile(token, path string) error
	RemovePlaylistEntry(token string, index int) error
	PlayNext(token string) error
	CloseNativeMedia(token string) error
}

type Source interface {
	GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error)
}

type Options struct {
	RemoveDelay time.Duration
	TempRoot    string
}

type Queue struct {
	player Player
	source Source
	delay  time.Duration
	root   string

	mu      sync.Mutex
	current *run
}

func New(player Player, source Source, opts Options) *Queue {
	if opts.RemoveDelay <= 0 {
		opts.RemoveDelay = DefaultRemoveDelay
	}
	if opts.TempRoot == "" {
		opts.TempRoot = os.TempDir()
	}
	return &Queue{player: player, source: source, delay: opts.RemoveDelay, root: opts.TempRoot}
}

func Sweep(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), dirPrefix) {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > sweepAge {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

func (q *Queue) Play(parent context.Context, bucket string, items []adminv1.ObjectSummary, notify func(error)) error {
	q.Stop()
	if len(items) == 0 || parent.Err() != nil {
		return nil
	}
	dir, err := os.MkdirTemp(q.root, dirPrefix)
	if err != nil {
		return fmt.Errorf("mediaqueue: create temp folder: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	r := &run{q: q, ctx: ctx, cancel: cancel, bucket: bucket, items: items, dir: dir, notify: notify,
		files: map[int]string{}, timers: map[int]*time.Timer{}, entries: []int{0}, next: 1}
	q.mu.Lock()
	q.current = r
	q.mu.Unlock()
	result, err := q.player.OpenNativePlaylist(bucket, []string{items[0].Key}, mediaservice.OpenOptions{OnState: r.onState})
	if err != nil {
		r.finish(false)
		return err
	}
	r.mu.Lock()
	r.token = result.Token
	r.mu.Unlock()
	r.prefetch()
	return nil
}

func (q *Queue) Stop() {
	q.mu.Lock()
	r := q.current
	q.current = nil
	q.mu.Unlock()
	if r != nil {
		r.finish(true)
	}
}

type run struct {
	q      *Queue
	ctx    context.Context
	cancel context.CancelFunc
	bucket string
	items  []adminv1.ObjectSummary
	dir    string
	notify func(error)

	mu       sync.Mutex
	token    string
	files    map[int]string
	timers   map[int]*time.Timer
	entries  []int
	next     int
	pos      int
	ended    bool
	fetching bool
	finished bool
}

func (r *run) report(err error) {
	if r.notify != nil && err != nil {
		r.notify(err)
	}
}

func (r *run) onState(st *mediaservice.State) {
	switch st.Status {
	case nativeplayer.StatusClosed:
		go r.finish(true)
		return
	case nativeplayer.StatusFailed:
		r.report(ErrPlayback)
		go r.finish(true)
		return
	}
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return
	}
	if r.token == "" {
		r.token = st.Token
	}
	r.ended = st.Status == nativeplayer.StatusEnded
	if st.PlaylistPos >= 0 && st.PlaylistPos < len(r.entries) {
		r.pos = r.entries[st.PlaylistPos]
	}
	for _, index := range r.entries {
		if index < r.pos && r.timers[index] == nil {
			r.scheduleRemoval(index)
		}
	}
	r.mu.Unlock()
	r.prefetch()
}

func (r *run) scheduleRemoval(index int) {
	r.timers[index] = time.AfterFunc(r.q.delay, func() { r.remove(index) })
}

func (r *run) remove(index int) {
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return
	}
	if len(r.entries) == 0 || r.entries[0] != index || r.pos <= index {
		if r.pos <= index {
			delete(r.timers, index)
		} else {
			r.scheduleRemoval(index)
		}
		r.mu.Unlock()
		return
	}
	r.entries = r.entries[1:]
	file := r.files[index]
	delete(r.files, index)
	delete(r.timers, index)
	token := r.token
	r.mu.Unlock()
	_ = r.q.player.RemovePlaylistEntry(token, 0)
	removeFile(file)
}

func (r *run) prefetch() {
	r.mu.Lock()
	if r.fetching || r.finished {
		r.mu.Unlock()
		return
	}
	r.fetching = true
	r.mu.Unlock()
	go r.fetchLoop()
}

func (r *run) fetchLoop() {
	for {
		r.mu.Lock()
		if r.finished || r.next >= len(r.items) || r.queuedAhead() > 0 {
			r.fetching = false
			r.mu.Unlock()
			return
		}
		index := r.next
		r.mu.Unlock()

		file, err := r.download(index)

		r.mu.Lock()
		r.next++
		if r.finished {
			r.fetching = false
			r.mu.Unlock()
			removeFile(file)
			return
		}
		if err != nil {
			r.mu.Unlock()
			if r.ctx.Err() == nil {
				r.report(fmt.Errorf("%w: %s: %w", ErrDownload, path.Base(r.items[index].Key), err))
			}
			continue
		}
		atEnd := r.ended && len(r.entries) > 0 && r.entries[len(r.entries)-1] == r.pos
		r.files[index] = file
		r.entries = append(r.entries, index)
		token := r.token
		r.mu.Unlock()
		if err := r.q.player.AppendLocalFile(token, file); err == nil && atEnd {
			_ = r.q.player.PlayNext(token)
		}
	}
}

func (r *run) queuedAhead() int {
	ahead := 0
	for _, index := range r.entries {
		if index > r.pos {
			ahead++
		}
	}
	return ahead
}

func (r *run) download(index int) (string, error) {
	item := r.items[index]
	folder := filepath.Join(r.dir, fmt.Sprintf("%03d", index))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(folder, safeName(item.Key))
	part := target + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	stream, err := r.q.source.GetObject(r.ctx, r.bucket, item.Key, adminv1.GetObjectOptions{IfMatch: item.ETag})
	if err == nil {
		stop := context.AfterFunc(r.ctx, func() { _ = stream.Body.Close() })
		_, err = io.Copy(file, stream.Body)
		stop()
		_ = stream.Body.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = r.ctx.Err()
	}
	if err == nil {
		err = os.Rename(part, target)
	}
	if err != nil {
		_ = os.Remove(part)
		_ = os.Remove(folder)
		return "", err
	}
	return target, nil
}

func (r *run) finish(closePlayer bool) {
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return
	}
	r.finished = true
	for _, t := range r.timers {
		t.Stop()
	}
	files := make([]string, 0, len(r.files))
	for _, f := range r.files {
		files = append(files, f)
	}
	token := r.token
	r.mu.Unlock()
	r.cancel()
	if closePlayer && token != "" {
		_ = r.q.player.CloseNativeMedia(token)
	}
	r.q.mu.Lock()
	if r.q.current == r {
		r.q.current = nil
	}
	r.q.mu.Unlock()
	dir := r.dir
	time.AfterFunc(r.q.delay, func() {
		for _, f := range files {
			removeFile(f)
		}
		_ = os.RemoveAll(dir)
	})
}

func removeFile(file string) {
	if file == "" {
		return
	}
	_ = os.Remove(file)
	_ = os.Remove(filepath.Dir(file))
}

func safeName(key string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0, '\n', '\r':
			return '_'
		}
		return r
	}, path.Base(key))
	if name == "" || name == "." || name == ".." {
		return "media"
	}
	return name
}
