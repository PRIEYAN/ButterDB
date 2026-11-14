package bucketrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type Source interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Repository struct {
	db Source
}

func New(db Source) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("bucketrepo: %w: database", errs.ErrMissingDependency)
	}
	return &Repository{db: db}, nil
}

const selectLive = `SELECT name, channel_id, policy, encryption_default, cache_max_age_seconds, created_date,
  object_count, stored_bytes, plaintext_bytes
FROM buckets WHERE deleted_msg_id = 0`

const (
	selectLiveByName    = selectLive + ` AND name = ?1`
	selectLiveByChannel = selectLive + ` AND channel_id = ?1`
	selectLiveOrdered   = selectLive + ` ORDER BY name`
)

type rowScanner interface {
	Scan(dest ...any) error
}

func (r *Repository) Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error) {
	info, found, err := r.lookupOne(ctx, selectLiveByName, string(name))
	if err != nil {
		return bucket.Info{}, false, fmt.Errorf("bucketrepo: lookup %q: %w", string(name), err)
	}
	return info, found, nil
}

func (r *Repository) LookupChannel(ctx context.Context, channelID int64) (bucket.Info, bool, error) {
	info, found, err := r.lookupOne(ctx, selectLiveByChannel, channelID)
	if err != nil {
		return bucket.Info{}, false, fmt.Errorf("bucketrepo: lookup channel %d: %w", channelID, err)
	}
	return info, found, nil
}

func (r *Repository) lookupOne(ctx context.Context, statement string, arg any) (bucket.Info, bool, error) {
	var info bucket.Info
	found := true
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		err := scanInfo(tx.QueryRowContext(ctx, statement, arg), &info)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		return bucket.Info{}, false, err
	}
	return info, found, nil
}

func (r *Repository) ListLive(ctx context.Context) ([]bucket.Info, error) {
	infos := []bucket.Info{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectLiveOrdered)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var info bucket.Info
			if err := scanInfo(rows, &info); err != nil {
				return err
			}
			infos = append(infos, info)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("bucketrepo: list live buckets: %w", err)
	}
	return infos, nil
}

func scanInfo(row rowScanner, info *bucket.Info) error {
	var name string
	if err := row.Scan(&name, &info.ChannelID, &info.Policy, &info.DefaultEncryption, &info.CacheMaxAge,
		&info.CreatedDate, &info.ObjectCount, &info.StoredBytes, &info.PlaintextBytes); err != nil {
		return err
	}
	info.Name = bucket.BucketName(name)
	return nil
}
