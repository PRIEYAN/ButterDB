package conn

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

type gotdClient interface {
	Run(ctx context.Context, f func(ctx context.Context) error) error
	API() *tg.Client
	Config() tg.Config
	Pool(max int64) (telegram.CloseInvoker, error)
	DC(ctx context.Context, dc int, max int64) (telegram.CloseInvoker, error)
	MediaOnly(ctx context.Context, dc int, max int64) (telegram.CloseInvoker, error)
}

type clientFactory func() (gotdClient, error)

type Dependencies struct {
	APIID       int
	APIHash     string
	Sessions    scope.SessionStore
	DeviceModel string
	TestDC      bool
	Clock       clock.Clock
	Logger      *slog.Logger
	Activity    *Activity
}

type Connection struct {
	deps    Dependencies
	factory clientFactory
	live    *liveConn

	baseCancel context.CancelFunc
	dials      *flightGroup[telegram.CloseInvoker]

	mu     sync.Mutex
	client gotdClient

	mediaMu     sync.Mutex
	pools       map[int]telegram.CloseInvoker
	poolRetryAt map[int]time.Time
}

var (
	_ scope.Invoker = (*Connection)(nil)
)

func New(deps Dependencies) (*Connection, error) {
	if err := validate(deps); err != nil {
		return nil, err
	}
	return newWithFactory(deps, telegramFactory(deps)), nil
}

func validate(deps Dependencies) error {
	switch {
	case deps.APIID <= 0:
		return fmt.Errorf("conn: %w: api_id", errs.ErrMissingDependency)
	case deps.APIHash == "":
		return fmt.Errorf("conn: %w: api_hash", errs.ErrMissingDependency)
	case deps.Sessions == nil:
		return fmt.Errorf("conn: %w: sessions", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return fmt.Errorf("conn: %w: clock", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return fmt.Errorf("conn: %w: logger", errs.ErrMissingDependency)
	case deps.Activity == nil:
		return fmt.Errorf("conn: %w: activity", errs.ErrMissingDependency)
	}
	return nil
}

func newWithFactory(deps Dependencies, factory clientFactory) *Connection {
	base, cancel := context.WithCancel(context.Background())
	c := &Connection{
		deps:        deps,
		factory:     factory,
		baseCancel:  cancel,
		dials:       newFlightGroup[telegram.CloseInvoker](base, poolDialTimeout),
		pools:       make(map[int]telegram.CloseInvoker),
		poolRetryAt: make(map[int]time.Time),
	}
	c.live = newLiveConn(c.runScope, deps.Logger)
	return c
}

func telegramFactory(deps Dependencies) clientFactory {
	return func() (gotdClient, error) {
		options := telegram.Options{
			SessionStorage: sessionStorage{store: deps.Sessions},
			UpdateHandler:  deps.Activity,
			Device:         telegram.DeviceConfig{DeviceModel: deps.DeviceModel},
		}
		if deps.TestDC {
			options.DCList = dcs.Test()
		}
		return telegram.NewClient(deps.APIID, deps.APIHash, options), nil
	}
}

func (c *Connection) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("conn: start: %w", err)
	}
	if c.live.isClosed() {
		return fmt.Errorf("conn: start: %w", errScopeClosed)
	}
	return nil
}

func (c *Connection) Close(ctx context.Context) error {
	c.baseCancel()
	c.closeMediaConns()
	liveErr := c.live.closeWait(ctx)
	dialErr := c.dials.close(ctx)
	c.closeMediaConns()
	for _, err := range []error{liveErr, dialErr} {
		if err != nil {
			return fmt.Errorf("conn: close: %w", err)
		}
	}
	return nil
}

func (c *Connection) runScope(runCtx context.Context, ready func()) error {
	client, err := c.factory()
	if err != nil {
		return fmt.Errorf("conn: connect: %w", err)
	}
	return client.Run(runCtx, func(rctx context.Context) error {
		defer c.closeMediaConns()
		c.mu.Lock()
		c.client = client
		c.mu.Unlock()
		ready()
		<-rctx.Done()
		return rctx.Err()
	})
}

func (c *Connection) acquire(ctx context.Context) (gotdClient, error) {
	if err := c.live.acquire(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("conn: client unavailable after connect")
	}
	return client, nil
}

func (c *Connection) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	client, err := c.acquire(ctx)
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(fn(ctx, client.API()))
}

func (c *Connection) InvokeMedia(ctx context.Context, dcID int, fn func(ctx context.Context, api *tg.Client) error) error {
	client, err := c.acquire(ctx)
	if err != nil {
		return normalizeError(err)
	}
	api, pooled := c.mediaAPI(ctx, client, dcID)
	err = fn(ctx, api)
	if pooled && isFileMigrate(err) {
		err = fn(ctx, client.API())
	}
	return normalizeError(err)
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if wait, ok := telegram.AsFloodWait(err); ok {
		return &tgclient.FloodWaitError{Duration: wait}
	}
	return err
}
