package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/readpath/blockcache"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/readpath/rangereader"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	blockCacheDirName     = "blocks"
	blockCacheDirMode     = 0o700
	readAheadBlocks       = 8
	defaultRangeCacheSize = 64 * 1024 * 1024
)

var (
	_ lifecycle.Component           = (*Read)(nil)
	_ objectreader.PeerResolver     = (*channelPeers)(nil)
	_ objectreader.KeyProvider      = (*readKeys)(nil)
	_ objectreader.DocumentResolver = tgclient.RangeReader(nil)
)

type ReadDependencies struct {
	Config   config.Config
	Database *sqlitedb.DB
	Sync     *Sync
	Telegram tgclient.ChannelRPC
	Origin   tgclient.RangeReader
	Ring     *keyring.Ring
	Files    blockcache.Files
	Clock    clock.Clock
	Sleeper  flow.Sleeper
	Random   io.Reader
	Logger   *slog.Logger
	Ranges   *rangereader.RangeReaderConfig
}

type Read struct {
	Cache   *blockcache.Cache
	Ranges  *rangereader.RangeReader
	Objects *objectreader.Opener
	Buckets *bucketrepo.Repository
	Live    *objectrepo.Repository

	files    blockcache.Files
	blockDir string
}

func NewRead(deps ReadDependencies) (*Read, error) {
	if err := validateReadDependencies(deps); err != nil {
		return nil, err
	}
	buckets, err := bucketrepo.New(deps.Database)
	if err != nil {
		return nil, fmt.Errorf("core: read: bucket repository: %w", err)
	}
	live, err := objectrepo.New(deps.Database)
	if err != nil {
		return nil, fmt.Errorf("core: read: object repository: %w", err)
	}
	r := &Read{Buckets: buckets, Live: live, files: deps.Files, blockDir: filepath.Join(deps.Config.Cache.Dir, blockCacheDirName)}
	if err := r.assemble(deps, buckets); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Read) assemble(deps ReadDependencies, buckets *bucketrepo.Repository) error {
	var err error
	r.Cache, err = blockcache.New(blockcache.Dependencies{
		Origin:   deps.Origin,
		Files:    deps.Files,
		Logger:   deps.Logger,
		Dir:      r.blockDir,
		MaxBytes: int64(deps.Config.Cache.BlocksMaxBytes),
	})
	if err != nil {
		return fmt.Errorf("core: read: block cache: %w", err)
	}
	r.Ranges, err = rangereader.NewRangeReader(rangereader.Dependencies{
		Client:   r.Cache,
		Resolver: deps.Origin,
		Clock:    deps.Clock,
		Sleeper:  deps.Sleeper,
		Random:   deps.Random,
		Logger:   deps.Logger,
	}, rangeReaderConfig(deps))
	if err != nil {
		return fmt.Errorf("core: read: range reader: %w", err)
	}
	r.Objects, err = objectreader.New(objectreader.Dependencies{
		Peers:     &channelPeers{channels: deps.Sync.Channels, resolver: deps.Telegram},
		Documents: deps.Origin,
		Stored:    r.Ranges,
		Keys:      &readKeys{repo: buckets, ring: deps.Ring},
		Clock:     deps.Clock,
	})
	if err != nil {
		return fmt.Errorf("core: read: object reader: %w", err)
	}
	return nil
}

func rangeReaderConfig(deps ReadDependencies) rangereader.RangeReaderConfig {
	if deps.Ranges != nil {
		return *deps.Ranges
	}
	return rangereader.RangeReaderConfig{MaxCacheBytes: defaultRangeCacheSize, ReadAhead: readAheadBlocks}
}

func (r *Read) Start(ctx context.Context) error {
	if err := r.files.MkdirAll(r.blockDir, blockCacheDirMode); err != nil {
		return fmt.Errorf("core: read: block cache directory: %w", err)
	}
	if err := r.Cache.Start(ctx); err != nil {
		return fmt.Errorf("core: read: start block cache: %w", err)
	}
	if err := r.Ranges.Start(ctx); err != nil {
		_ = r.Cache.Close(ctx)
		return fmt.Errorf("core: read: start range reader: %w", err)
	}
	return nil
}

func (r *Read) Close(ctx context.Context) error {
	return errors.Join(r.Objects.Close(ctx), r.Ranges.Close(ctx), r.Cache.Close(ctx))
}

func (r *Read) Open(ctx context.Context, bucketName, key string) (object.ObjectMeta, objectreader.Opened, error) {
	info, found, err := r.Buckets.Lookup(ctx, bucket.BucketName(bucketName))
	if err != nil {
		return object.ObjectMeta{}, nil, fmt.Errorf("core: read: look up bucket %q: %w", bucketName, err)
	}
	if !found {
		return object.ObjectMeta{}, nil, fmt.Errorf("core: read: bucket %q: %w", bucketName, errs.ErrNoSuchBucket)
	}
	meta, err := r.Live.Get(ctx, info.ChannelID, key)
	if err != nil {
		return object.ObjectMeta{}, nil, fmt.Errorf("core: read: %w", err)
	}
	opened, err := r.Objects.Open(ctx, meta)
	if err != nil {
		return object.ObjectMeta{}, nil, fmt.Errorf("core: read: open %q: %w", key, err)
	}
	return meta, opened, nil
}

type readKeys struct {
	repo *bucketrepo.Repository
	ring *keyring.Ring
}

func (k *readKeys) DEK(ctx context.Context, bucketChannelID int64, keyID string) (objectreader.DEK, error) {
	key, found, err := k.repo.Key(ctx, bucketChannelID, keyID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("core: read: key %q of channel %d: %w", keyID, bucketChannelID, errs.ErrKeyUnavailable)
	}
	dek, err := k.ring.UnwrapDEK(key.WrappedDEK, key.KeyCheck, bucketChannelID, keyID)
	if err != nil {
		return nil, err
	}
	return dek, nil
}

func validateReadDependencies(deps ReadDependencies) error {
	missing := ""
	switch {
	case deps.Database == nil:
		missing = "projection database"
	case deps.Sync == nil:
		missing = "sync"
	case deps.Telegram == nil:
		missing = "telegram"
	case deps.Origin == nil:
		missing = "range origin"
	case deps.Ring == nil:
		missing = "server key"
	case deps.Files == nil:
		missing = "filesystem"
	case deps.Config.Cache.Dir == "":
		missing = "cache directory"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Sleeper == nil:
		missing = "sleeper"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return fmt.Errorf("core: read: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}
