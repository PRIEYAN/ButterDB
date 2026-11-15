package blockcache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/readpath"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var _ readpath.DocumentRangeReader = (*Cache)(nil)

type Dependencies struct {
	Origin     readpath.DocumentRangeReader
	Files      Files
	Logger     *slog.Logger
	Dir        string
	MaxBytes   int64
	MaxEntries int
}

type Stats struct {
	Hits       int64
	Misses     int64
	Evictions  int64
	FillBytes  int64
	UsedBytes  int64
	LimitBytes int64
	Entries    int
}

type Cache struct {
	origin  readpath.DocumentRangeReader
	logger  *slog.Logger
	store   *store
	flights *flights
	stats   *counters
}

func New(deps Dependencies) (*Cache, error) {
	switch {
	case deps.Origin == nil:
		return nil, fmt.Errorf("blockcache: %w: origin", errs.ErrMissingDependency)
	case deps.Files == nil:
		return nil, fmt.Errorf("blockcache: %w: files", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("blockcache: %w: logger", errs.ErrMissingDependency)
	case deps.Dir == "":
		return nil, fmt.Errorf("blockcache: %w: dir", errs.ErrMissingDependency)
	case deps.MaxBytes < 0:
		return nil, fmt.Errorf("blockcache: negative byte budget %d", deps.MaxBytes)
	case deps.MaxEntries < 0:
		return nil, fmt.Errorf("blockcache: negative entry cap %d", deps.MaxEntries)
	}
	maxEntries := deps.MaxEntries
	if maxEntries == 0 {
		maxEntries = defaultMaxEntries
	}
	stats := &counters{}
	return &Cache{
		origin:  deps.Origin,
		logger:  deps.Logger,
		store:   newStore(deps.Files, deps.Dir, deps.MaxBytes, maxEntries, deps.Logger, stats),
		flights: newFlights(),
		stats:   stats,
	}, nil
}

func (c *Cache) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.store.init()
	return nil
}

func (c *Cache) Close(context.Context) error {
	c.flights.close()
	return nil
}

func (c *Cache) enabled() bool {
	return c.store.maxBytes > 0
}

func keyFor(ref tgclient.DocumentRef, offset int64, length int) blockKey {
	return blockKey{channelID: ref.Peer.ChannelID, messageID: ref.MsgID, offset: offset, length: length}
}

func (c *Cache) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if !c.enabled() || !cacheableShape(offset, len(dst)) {
		return c.origin.ReadDocumentRange(ctx, ref, offset, dst)
	}
	key := keyFor(ref, offset, len(dst))
	if c.store.read(key, dst) {
		c.stats.hits.Add(1)
		return len(dst), nil
	}
	c.stats.misses.Add(1)
	data, err := c.flights.do(ctx, key, func(loadCtx context.Context) ([]byte, error) {
		return c.fill(loadCtx, ref, key)
	})
	if err != nil {
		return 0, err
	}
	return copy(dst, data), nil
}

func (c *Cache) fill(ctx context.Context, ref tgclient.DocumentRef, key blockKey) ([]byte, error) {
	block := make([]byte, key.length)
	n, err := c.origin.ReadDocumentRange(ctx, ref, key.offset, block)
	if err != nil {
		return nil, err
	}
	if n != len(block) {
		return nil, fmt.Errorf("blockcache: origin returned %d of %d bytes at %d", n, len(block), key.offset)
	}
	if err := c.store.write(key, block); err != nil {
		c.logger.Debug("blockcache: block not cached", "error", err)
	}
	return block, nil
}

func (c *Cache) Warm(ref tgclient.DocumentRef, offset int64, block []byte) error {
	if !c.enabled() || !cacheableShape(offset, len(block)) {
		return nil
	}
	return c.store.write(keyFor(ref, offset, len(block)), block)
}

func (c *Cache) Stats() Stats {
	used, limit, entries := c.store.usage()
	return Stats{
		Hits:       c.stats.hits.Load(),
		Misses:     c.stats.misses.Load(),
		Evictions:  c.stats.evictions.Load(),
		FillBytes:  c.stats.fillBytes.Load(),
		UsedBytes:  used,
		LimitBytes: limit,
		Entries:    entries,
	}
}

func (c *Cache) Clear(ctx context.Context) error {
	return c.store.clear(ctx)
}
