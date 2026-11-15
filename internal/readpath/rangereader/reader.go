package rangereader

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	blockBytes                   = 1024 * 1024
	defaultRangeCacheBytes       = 64 * 1024 * 1024
	rangeUploadBoundary    int64 = blockBytes
	defaultBackgroundSlots       = 4
	openingChunkBytes      int64 = 256 * 1024
)

var _ readpath.StoredReader = (*RangeReader)(nil)

type DocumentResolver interface {
	ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error)
}

type Dependencies struct {
	Client   readpath.DocumentRangeReader
	Resolver DocumentResolver
	Clock    clock.Clock
	Sleeper  Sleeper
	Random   io.Reader
	Logger   *slog.Logger
}

type RangeReaderConfig struct {
	MaxCacheBytes int64
	Retry         *RetryPolicy
	OnFloodWait   func(wait time.Duration)
	ReadAhead     int
	Background    bool
}

type RangeReader struct {
	deps        Dependencies
	cache       *blockCache
	retry       retryer
	onFloodWait func(time.Duration)
	background  bool
	readAhead   int
	lane        chan struct{}
	stats       rangeStats

	mu      sync.Mutex
	runCtx  context.Context
	cancel  context.CancelFunc
	closed  bool
	flights map[string]*flight
	running sync.WaitGroup

	refMu        sync.Mutex
	refs         map[string]tgclient.DocumentRef
	refreshGroup singleflight.Group
}

func NewRangeReader(deps Dependencies, cfg RangeReaderConfig) (*RangeReader, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	policy := defaultRangeRetryPolicy()
	if cfg.Retry != nil {
		policy = *cfg.Retry
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	maxCache := cfg.MaxCacheBytes
	if maxCache == 0 {
		maxCache = defaultRangeCacheBytes
	}
	return &RangeReader{
		deps:        deps,
		cache:       newBlockCache(maxCache),
		retry:       retryer{policy: policy, sleeper: deps.Sleeper, random: deps.Random, logger: deps.Logger},
		onFloodWait: cfg.OnFloodWait,
		background:  cfg.Background,
		readAhead:   max(cfg.ReadAhead, 0),
		lane:        make(chan struct{}, max(cfg.ReadAhead, defaultBackgroundSlots)),
		flights:     make(map[string]*flight),
		refs:        make(map[string]tgclient.DocumentRef),
	}, nil
}

func validateDependencies(deps Dependencies) error {
	switch {
	case deps.Client == nil:
		return fmt.Errorf("rangereader: %w: client", errs.ErrMissingDependency)
	case deps.Resolver == nil:
		return fmt.Errorf("rangereader: %w: resolver", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return fmt.Errorf("rangereader: %w: clock", errs.ErrMissingDependency)
	case deps.Sleeper == nil:
		return fmt.Errorf("rangereader: %w: sleeper", errs.ErrMissingDependency)
	case deps.Random == nil:
		return fmt.Errorf("rangereader: %w: random", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return fmt.Errorf("rangereader: %w: logger", errs.ErrMissingDependency)
	}
	return nil
}

func (r *RangeReader) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.runCtx != nil {
		return fmt.Errorf("rangereader: %w: start after start or close", errs.ErrServiceUnavailable)
	}
	r.runCtx, r.cancel = context.WithCancel(ctx)
	return nil
}

func (r *RangeReader) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		r.running.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("rangereader: close: %w", ctx.Err())
	}
}

func (r *RangeReader) Stats() RangeStats {
	return r.stats.snapshot()
}

func (r *RangeReader) activeContext() (context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runCtx == nil || r.closed {
		return nil, fmt.Errorf("rangereader: %w: not running", errs.ErrServiceUnavailable)
	}
	return r.runCtx, r.runCtx.Err()
}

func (r *RangeReader) ReadStoredAt(ctx context.Context, ref tgclient.DocumentRef, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := r.activeContext(); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, fmt.Errorf("rangereader: %w: negative offset", errs.ErrInvalidRange)
	}
	if ref.Size <= 0 || off >= ref.Size {
		return 0, io.EOF
	}

	want := len(p)
	if available := ref.Size - off; int64(want) > available {
		want = int(available)
	}

	done := 0
	for done < want {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		absolute := off + int64(done)
		block, blockOffset, err := r.span(ctx, ref, absolute, blockStartFor(absolute))
		if err != nil {
			return done, err
		}
		inside := int(absolute - blockOffset)
		if inside >= len(block) {
			return done, io.EOF
		}
		done += copy(p[done:want], block[inside:])
	}
	r.readAheadAfter(ref, off+int64(done))
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

func (r *RangeReader) readAheadAfter(ref tgclient.DocumentRef, nextOffset int64) {
	if r.readAhead <= 0 || nextOffset <= 0 || nextOffset >= ref.Size {
		return
	}
	next := blockStartFor(nextOffset)
	if next < nextOffset {
		next += rangeUploadBoundary
	}
	r.readAheadFrom(ref, next)
}

func (r *RangeReader) readAheadFrom(ref tgclient.DocumentRef, next int64) {
	if r.readAhead <= 0 || next >= ref.Size {
		return
	}
	end := min(next+int64(r.readAhead)*rangeUploadBoundary, ref.Size)

	r.mu.Lock()
	var missing []int64
	for start := next; start < end; start += rangeUploadBoundary {
		key := blockKey(ref, start)
		if _, ok := r.flights[key]; ok {
			continue
		}
		if _, ok := r.cache.get(key); ok {
			continue
		}
		missing = append(missing, start)
	}
	if free := cap(r.lane) - len(r.lane); len(missing) > free {
		r.cancelStaleLocked(docKey(ref), next, end, len(missing)-free)
	}
	r.mu.Unlock()

	for _, start := range missing {
		r.prefetch(ref, start, true)
	}
}

func (r *RangeReader) cancelStaleLocked(doc string, next, end int64, n int) {
	var stale []*flight
	for _, f := range r.flights {
		if f.speculative && f.doc == doc && !f.isClaimed() && (f.start < next || f.start >= end) {
			stale = append(stale, f)
		}
	}
	distance := func(f *flight) int64 {
		if f.start < next {
			return next - f.start
		}
		return f.start - end
	}
	slices.SortFunc(stale, func(a, b *flight) int {
		return cmp.Compare(distance(b), distance(a))
	})
	for _, f := range stale[:min(n, len(stale))] {
		f.cancel()
	}
}

func (r *RangeReader) prefetchBlock(ref tgclient.DocumentRef, blockStart int64) {
	r.prefetch(ref, blockStart, false)
}

func (r *RangeReader) prefetch(ref tgclient.DocumentRef, blockStart int64, speculative bool) {
	if blockStart < 0 || blockStart >= ref.Size {
		return
	}
	key := blockKey(ref, blockStart)
	if _, ok := r.cache.get(key); ok {
		return
	}
	f, created, err := r.flightFor(key, ref, blockStart, true, speculative)
	if err == nil && created {
		go r.run(f, ref, key, blockLimit(ref.Size, blockStart))
	}
}

func (r *RangeReader) span(ctx context.Context, ref tgclient.DocumentRef, absolute, blockStart int64) ([]byte, int64, error) {
	if r.wantsOpeningPrefix(ref, absolute, blockStart) {
		data, err := r.fetch(ctx, ref, openingKey(ref), 0, int(openingChunkBytes))
		if err == nil {
			// The full block is filled behind the prefix so the next read is warm, at the cost of re-fetching the prefix.
			r.prefetch(ref, 0, false)
			return data, 0, nil
		}
	}
	data, err := r.block(ctx, ref, blockStart)
	return data, blockStart, err
}

func (r *RangeReader) wantsOpeningPrefix(ref tgclient.DocumentRef, absolute, blockStart int64) bool {
	if r.background || blockStart != 0 || absolute >= openingChunkBytes {
		return false
	}
	if ref.Size <= openingChunkBytes {
		return false
	}
	_, full := r.cache.get(blockKey(ref, 0))
	return !full
}

func (r *RangeReader) block(ctx context.Context, ref tgclient.DocumentRef, blockStart int64) ([]byte, error) {
	return r.fetch(ctx, ref, blockKey(ref, blockStart), blockStart, blockLimit(ref.Size, blockStart))
}

func (r *RangeReader) fetch(ctx context.Context, ref tgclient.DocumentRef, key string, start int64, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, io.EOF
	}
	// A seek can cancel a speculative flight just before this reader claims it, so one retry with a fresh flight always suffices.
	for attempt := 0; attempt < 2; attempt++ {
		if data, ok := r.cache.get(key); ok {
			r.stats.hit()
			return data, nil
		}
		f, created, err := r.flightFor(key, ref, start, r.background, false)
		if err != nil {
			return nil, err
		}
		if created {
			go r.run(f, ref, key, limit)
		}
		select {
		case <-f.done:
			if f.err == nil {
				return f.data, nil
			}
			if !errors.Is(f.err, context.Canceled) || ctx.Err() != nil {
				return nil, f.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, context.Canceled
}

func (r *RangeReader) flightFor(key string, ref tgclient.DocumentRef, start int64, background, speculative bool) (*flight, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runCtx == nil || r.closed {
		return nil, false, fmt.Errorf("rangereader: %w: not running", errs.ErrServiceUnavailable)
	}
	if err := r.runCtx.Err(); err != nil {
		return nil, false, err
	}
	if f, ok := r.flights[key]; ok {
		if !speculative {
			f.claim(!background)
		}
		return f, false, nil
	}
	f := newFlight(r.runCtx, docKey(ref), start, background, speculative)
	r.flights[key] = f
	r.running.Add(1)
	return f, true, nil
}

// The block enters the cache before the flight leaves the map, so a reader arriving in between never starts a duplicate fetch.
func (r *RangeReader) run(f *flight, ref tgclient.DocumentRef, key string, limit int) {
	defer r.running.Done()
	r.stats.begin()
	data, err := r.fetchSpan(f, ref, limit)
	r.stats.end(err)
	if err == nil {
		r.cache.put(key, data)
	}
	r.mu.Lock()
	if r.flights[key] == f {
		delete(r.flights, key)
	}
	r.mu.Unlock()
	f.data, f.err = data, err
	close(f.done)
	f.cancel()
}

func (r *RangeReader) fetchSpan(f *flight, ref tgclient.DocumentRef, limit int) ([]byte, error) {
	started := r.deps.Clock.Now()
	defer func() {
		if !f.background {
			r.deps.Logger.Debug("rangereader: fetched block from telegram", "offset", f.start, "bytes", limit, "elapsed", r.deps.Clock.Now().Sub(started))
		}
	}()

	buf := make([]byte, limit)
	err := r.retry.do(f.ctx, func() error {
		n, err := r.fetchBlockOnce(f, ref, buf)
		if n == len(buf) && (err == nil || errors.Is(err, io.EOF)) {
			return nil
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		if wait, ok := floodWaitDuration(err); ok && r.onFloodWait != nil {
			r.onFloodWait(wait)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// The lane slot is held for the request only, never across a retry backoff.
func (r *RangeReader) fetchBlockOnce(f *flight, ref tgclient.DocumentRef, buf []byte) (int, error) {
	release, err := r.acquireLane(f)
	if err != nil {
		return 0, err
	}
	defer release()
	current := r.currentRef(ref)
	started := r.deps.Clock.Now()
	n, err := r.deps.Client.ReadDocumentRange(f.ctx, current, f.start, buf)
	if err == nil {
		r.stats.observe(r.deps.Clock.Now().Sub(started))
	}
	if !errors.Is(err, tgclient.ErrFileReferenceExpired) {
		return n, err
	}
	fresh, refreshErr := r.refreshRef(f.ctx, current)
	if refreshErr != nil {
		return 0, fmt.Errorf("rangereader: refresh file reference: %w: %w", err, refreshErr)
	}
	return r.deps.Client.ReadDocumentRange(f.ctx, fresh, f.start, buf)
}

func (r *RangeReader) currentRef(ref tgclient.DocumentRef) tgclient.DocumentRef {
	r.refMu.Lock()
	defer r.refMu.Unlock()
	if fresh, ok := r.refs[docKey(ref)]; ok {
		return fresh
	}
	return ref
}

func (r *RangeReader) refreshRef(ctx context.Context, stale tgclient.DocumentRef) (tgclient.DocumentRef, error) {
	key := docKey(stale)
	result, err, _ := r.refreshGroup.Do(key, func() (any, error) {
		if fresh := r.currentRef(stale); !bytes.Equal(fresh.FileReference, stale.FileReference) {
			return fresh, nil
		}
		fresh, err := r.deps.Resolver.ResolveDocument(ctx, stale.Peer, stale.MsgID)
		if err != nil {
			return nil, err
		}
		r.refMu.Lock()
		r.refs[key] = fresh
		r.refMu.Unlock()
		return fresh, nil
	})
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	return result.(tgclient.DocumentRef), nil
}

func (r *RangeReader) acquireLane(f *flight) (func(), error) {
	if !f.background || f.isPromoted() {
		return func() {}, nil
	}
	select {
	case r.lane <- struct{}{}:
		return func() { <-r.lane }, nil
	case <-f.promoted:
		return func() {}, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func blockStartFor(off int64) int64 {
	return (off / rangeUploadBoundary) * rangeUploadBoundary
}

func blockLimit(size, blockStart int64) int {
	if blockStart >= size {
		return 0
	}
	remaining := size - blockStart
	if remaining > rangeUploadBoundary {
		return blockBytes
	}
	return int(remaining)
}

func openingKey(ref tgclient.DocumentRef) string {
	return blockKey(ref, 0) + ":open"
}

func docKey(ref tgclient.DocumentRef) string {
	return strconv.FormatInt(ref.Peer.ChannelID, 10) + ":" + strconv.FormatInt(ref.MsgID, 10)
}

func blockKey(ref tgclient.DocumentRef, blockStart int64) string {
	return docKey(ref) + ":" + strconv.FormatInt(blockStart, 10)
}
