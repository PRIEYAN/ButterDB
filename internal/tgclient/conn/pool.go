package conn

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const MediaPoolSize = 4

const UploadThreads = 2 * MediaPoolSize

const poolRetryAfter = 30 * time.Second

const cdnPoolSize = 2

const poolDialTimeout = 30 * time.Second

func isFileMigrate(err error) bool {
	rpcErr, ok := tgerr.As(err)
	return ok && rpcErr.Type == "FILE_MIGRATE"
}

func isCDNOnly(cfg tg.Config, dcID int) bool {
	found := false
	for _, option := range cfg.DCOptions {
		if option.ID != dcID {
			continue
		}
		if !option.CDN {
			return false
		}
		found = true
	}
	return found
}

func (c *Connection) mediaAPI(ctx context.Context, client gotdClient, dcID int) (api *tg.Client, pooled bool) {
	if dcID <= 0 {
		return client.API(), false
	}
	invoker, err := c.filePool(ctx, client, dcID)
	if err != nil {
		return client.API(), false
	}
	return tg.NewClient(invoker), true
}

func (c *Connection) filePool(ctx context.Context, client gotdClient, dcID int) (telegram.CloseInvoker, error) {
	c.mediaMu.Lock()
	invoker, ok := c.pools[dcID]
	retryAt := c.poolRetryAt[dcID]
	c.mediaMu.Unlock()
	if ok {
		return invoker, nil
	}
	if c.deps.Clock.Now().Before(retryAt) {
		return nil, fmt.Errorf("conn: file pool dc %d: dial backoff", dcID)
	}
	return c.dials.do(ctx, strconv.Itoa(dcID), func(dialCtx context.Context) (telegram.CloseInvoker, error) {
		return c.dialFilePool(dialCtx, client, dcID)
	})
}

func (c *Connection) dialFilePool(ctx context.Context, client gotdClient, dcID int) (telegram.CloseInvoker, error) {
	started := c.deps.Clock.Now()
	invoker, err := c.openPool(ctx, client, dcID)

	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	if err != nil {
		// Cancellation says nothing about the data center; only a refused dial earns the backoff.
		if !errors.Is(err, context.Canceled) {
			c.poolRetryAt[dcID] = c.deps.Clock.Now().Add(poolRetryAfter)
			c.deps.Logger.Warn("conn: file pool unavailable, using the primary connection", "dc", dcID, "error", err)
		}
		return nil, fmt.Errorf("conn: file pool dc %d: %w", dcID, err)
	}
	if existing, ok := c.pools[dcID]; ok {
		_ = invoker.Close()
		return existing, nil
	}
	c.pools[dcID] = invoker
	c.deps.Logger.Info("conn: file pool ready", "dc", dcID, "elapsed", c.deps.Clock.Now().Sub(started))
	return invoker, nil
}

func (c *Connection) openPool(ctx context.Context, client gotdClient, dcID int) (telegram.CloseInvoker, error) {
	cfg := client.Config()
	if isCDNOnly(cfg, dcID) {
		return client.MediaOnly(ctx, dcID, cdnPoolSize)
	}
	if cfg.ThisDC == 0 {
		return nil, fmt.Errorf("primary data center unknown")
	}
	if dcID == cfg.ThisDC {
		return client.Pool(MediaPoolSize)
	}
	return client.DC(ctx, dcID, MediaPoolSize)
}

func (c *Connection) closeMediaConns() {
	c.mediaMu.Lock()
	conns := make([]telegram.CloseInvoker, 0, len(c.pools))
	for _, pool := range c.pools {
		conns = append(conns, pool)
	}
	c.pools = make(map[int]telegram.CloseInvoker)
	c.mediaMu.Unlock()
	for _, pool := range conns {
		_ = pool.Close()
	}
}
