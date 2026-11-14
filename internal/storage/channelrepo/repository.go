package channelrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type Kind string

const (
	KindRegistry Kind = "registry"
	KindBucket   Kind = "bucket"
)

type Source interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	ErrInvalidChannel  = errors.New("invalid channel")
	ErrChannelNotFound = errors.New("channel not found")
)

type Channel struct {
	ChannelID              int64
	AccessHash             int64
	Kind                   Kind
	Title                  string
	Deleted                bool
	LastSyncedMsg          int64
	Pts                    int
	InitialSyncDone        bool
	NeedsProjectionRebuild bool
	LastReconciledAt       int64
}

type Repository struct {
	db Source
}

func New(db Source) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("channelrepo: %w: database", errs.ErrMissingDependency)
	}
	return &Repository{db: db}, nil
}

const selectChannel = `SELECT channel_id, access_hash, kind, title, state, last_synced_msg, pts, initial_sync_done,
  needs_projection_rebuild, last_reconciled_at FROM channels`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChannel(row rowScanner, c *Channel) error {
	var kind, state string
	var initialSyncDone, needsRebuild int
	if err := row.Scan(&c.ChannelID, &c.AccessHash, &kind, &c.Title, &state, &c.LastSyncedMsg, &c.Pts,
		&initialSyncDone, &needsRebuild, &c.LastReconciledAt); err != nil {
		return err
	}
	c.Kind = Kind(kind)
	c.Deleted = state == "deleted"
	c.InitialSyncDone = initialSyncDone != 0
	c.NeedsProjectionRebuild = needsRebuild != 0
	return nil
}

func (r *Repository) GetChannel(ctx context.Context, channelID int64) (Channel, bool, error) {
	var channel Channel
	found := true
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		err := scanChannel(tx.QueryRowContext(ctx, selectChannel+` WHERE channel_id = ?1`, channelID), &channel)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		return Channel{}, false, fmt.Errorf("channelrepo: get channel %d: %w", channelID, err)
	}
	return channel, found, nil
}

func (r *Repository) ChannelExists(ctx context.Context, channelID int64) (bool, error) {
	if channelID <= 0 {
		return false, fmt.Errorf("channelrepo: channel exists: %w: id %d", ErrInvalidChannel, channelID)
	}
	_, found, err := r.GetChannel(ctx, channelID)
	return found, err
}

func (r *Repository) ListChannels(ctx context.Context) ([]Channel, error) {
	channels := []Channel{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectChannel+` ORDER BY kind DESC, channel_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var channel Channel
			if err := scanChannel(rows, &channel); err != nil {
				return err
			}
			channels = append(channels, channel)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("channelrepo: list channels: %w", err)
	}
	return channels, nil
}

func (r *Repository) InsertChannel(ctx context.Context, channel Channel) error {
	return r.UpsertChannel(ctx, channel)
}

func (r *Repository) UpsertChannel(ctx context.Context, channel Channel) error {
	if err := validateUpsert(channel); err != nil {
		return fmt.Errorf("channelrepo: upsert channel: %w", err)
	}
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO channels (channel_id, kind, access_hash, title) VALUES (?1, ?2, ?3, ?4)
ON CONFLICT(channel_id) DO UPDATE SET access_hash = excluded.access_hash, title = excluded.title`,
			channel.ChannelID, string(channel.Kind), channel.AccessHash, channel.Title)
		return err
	})
	if err != nil {
		return fmt.Errorf("channelrepo: upsert channel %d: %w", channel.ChannelID, err)
	}
	return nil
}

func validateUpsert(channel Channel) error {
	if channel.ChannelID <= 0 {
		return fmt.Errorf("%w: id %d", ErrInvalidChannel, channel.ChannelID)
	}
	if channel.Kind != KindRegistry && channel.Kind != KindBucket {
		return fmt.Errorf("%w: kind %q", ErrInvalidChannel, channel.Kind)
	}
	if channel.AccessHash == 0 {
		return fmt.Errorf("%w: access hash is zero", ErrInvalidChannel)
	}
	return nil
}

func (r *Repository) UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error {
	if accessHash == 0 {
		return fmt.Errorf("channelrepo: update access hash: %w: access hash is zero", ErrInvalidChannel)
	}
	return r.writeOne(ctx, "update access hash", channelID,
		`UPDATE channels SET access_hash = ?2 WHERE channel_id = ?1`, channelID, accessHash)
}

func (r *Repository) SetNeedsProjectionRebuild(ctx context.Context, channelID int64, needed bool) error {
	return r.writeOne(ctx, "set needs projection rebuild", channelID,
		`UPDATE channels SET needs_projection_rebuild = ?2 WHERE channel_id = ?1`, channelID, boolInt(needed))
}

func (r *Repository) SetLastReconciledAt(ctx context.Context, channelID, at int64) error {
	return r.writeOne(ctx, "set last reconciled at", channelID,
		`UPDATE channels SET last_reconciled_at = ?2 WHERE channel_id = ?1`, channelID, at)
}

func (r *Repository) writeOne(ctx context.Context, what string, channelID int64, statement string, args ...any) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return execOne(ctx, tx, channelID, statement, args...)
	})
	if err != nil {
		return fmt.Errorf("channelrepo: %s %d: %w", what, channelID, err)
	}
	return nil
}

func execOne(ctx context.Context, tx Tx, channelID int64, statement string, args ...any) error {
	result, err := tx.ExecContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %d", ErrChannelNotFound, channelID)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
