package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
)

const rebuildReplayBatchSize = 256

var ErrRegistryChannelAlone = errors.New("rebuild: the registry channel rebuilds only together with every bucket channel")

type Dependencies struct {
	Registry projection.Registry
	Logger   *slog.Logger
}

type Result struct {
	Applied  int
	Rejected int
}

type Rebuilder struct {
	registry projection.Registry
	logger   *slog.Logger
}

type channelInfo struct {
	id       int64
	registry bool
	deleted  bool
}

func New(deps Dependencies) (*Rebuilder, error) {
	if deps.Registry == nil {
		return nil, fmt.Errorf("rebuild: %w: registry", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("rebuild: %w: logger", errs.ErrMissingDependency)
	}
	return &Rebuilder{registry: deps.Registry, logger: deps.Logger}, nil
}

func (r *Rebuilder) RebuildAll(ctx context.Context, w projection.Writer) (Result, error) {
	var total Result
	err := r.inWrite(ctx, w, func(tx *sql.Tx) error {
		var err error
		total, err = r.RebuildAllTx(ctx, tx)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return total, nil
}

func (r *Rebuilder) RebuildChannel(ctx context.Context, w projection.Writer, channelID int64) (Result, error) {
	var total Result
	err := r.inWrite(ctx, w, func(tx *sql.Tx) error {
		var err error
		total, err = r.RebuildChannelTx(ctx, tx, channelID)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return total, nil
}

func (r *Rebuilder) RebuildAllTx(ctx context.Context, tx projection.Tx) (Result, error) {
	channels, err := listLiveChannels(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	var total Result
	for _, channel := range channels {
		result, err := r.rebuildChannel(ctx, tx, channel)
		if err != nil {
			return Result{}, err
		}
		total.Applied += result.Applied
		total.Rejected += result.Rejected
	}
	return total, nil
}

func (r *Rebuilder) RebuildChannelTx(ctx context.Context, tx projection.Tx, channelID int64) (Result, error) {
	channel, err := loadChannel(ctx, tx, channelID)
	if err != nil {
		return Result{}, err
	}
	if channel.deleted {
		return Result{}, nil
	}
	if channel.registry {
		return Result{}, fmt.Errorf("rebuild: channel %d: %w", channelID, ErrRegistryChannelAlone)
	}
	return r.rebuildChannel(ctx, tx, channel)
}

func (r *Rebuilder) rebuildChannel(ctx context.Context, tx projection.Tx, channel channelInfo) (Result, error) {
	r.logger.Info("rebuild: channel starting", "channel_id", channel.id, "registry", channel.registry)
	if err := clearDerived(ctx, tx, channel); err != nil {
		return Result{}, err
	}
	result, err := r.replay(ctx, tx, channel.id, replayScope{})
	if err != nil {
		r.logger.Error("rebuild: channel failed", "channel_id", channel.id, "error", err)
		return Result{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE channels SET needs_projection_rebuild = 0 WHERE channel_id = ?`, channel.id); err != nil {
		return Result{}, fmt.Errorf("rebuild: clear rebuild flag for channel %d: %w", channel.id, err)
	}
	r.logger.Info("rebuild: channel completed", "channel_id", channel.id, "applied", result.Applied, "rejected", result.Rejected)
	return result, nil
}

func (r *Rebuilder) inWrite(ctx context.Context, w projection.Writer, fn func(tx *sql.Tx) error) error {
	if w == nil {
		return fmt.Errorf("rebuild: %w: writer", errs.ErrMissingDependency)
	}
	return w.Write(ctx, fn)
}

func listLiveChannels(ctx context.Context, tx projection.Tx) ([]channelInfo, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT channel_id, kind
		FROM channels
		WHERE state <> 'deleted'
		ORDER BY CASE kind WHEN 'registry' THEN 0 ELSE 1 END, channel_id
	`)
	if err != nil {
		return nil, fmt.Errorf("rebuild: list channels: %w", err)
	}
	defer rows.Close()
	var channels []channelInfo
	for rows.Next() {
		var channel channelInfo
		var kind string
		if err := rows.Scan(&channel.id, &kind); err != nil {
			return nil, fmt.Errorf("rebuild: scan channel: %w", err)
		}
		channel.registry = kind == "registry"
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rebuild: list channels: %w", err)
	}
	return channels, nil
}

func loadChannel(ctx context.Context, tx projection.Tx, channelID int64) (channelInfo, error) {
	var kind, state string
	err := tx.QueryRowContext(ctx, `SELECT kind, state FROM channels WHERE channel_id = ?`, channelID).Scan(&kind, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return channelInfo{}, fmt.Errorf("rebuild: channel %d: %w", channelID, errs.ErrNoSuchBucket)
	}
	if err != nil {
		return channelInfo{}, fmt.Errorf("rebuild: load channel %d: %w", channelID, err)
	}
	return channelInfo{id: channelID, registry: kind == "registry", deleted: state == "deleted"}, nil
}

const clearRejectsOfLoggedMessages = `
	DELETE FROM replay_log_rejects
	WHERE channel_id = ?1 AND msg_id IN (SELECT msg_id FROM replay_log WHERE channel_id = ?1)`

var clearBucketChannelStatements = []string{
	`DELETE FROM objects WHERE channel_id = ?1`,
	`DELETE FROM object_tombstones WHERE channel_id = ?1`,
	`DELETE FROM object_bodies WHERE channel_id = ?1`,
	`DELETE FROM projection_operations WHERE channel_id = ?1`,
	`UPDATE buckets SET object_count = 0, stored_bytes = 0, plaintext_bytes = 0 WHERE channel_id = ?1`,
	clearRejectsOfLoggedMessages,
}

var clearRegistryChannelStatements = []string{
	`DELETE FROM registry_markers WHERE channel_id = ?1`,
	`DELETE FROM projection_operations WHERE channel_id = ?1`,
	clearRejectsOfLoggedMessages,
}

var clearEveryBucketStatements = []string{
	`DELETE FROM encryption_keys`,
	`DELETE FROM buckets`,
}

func clearDerived(ctx context.Context, tx projection.Tx, channel channelInfo) error {
	statements := clearBucketChannelStatements
	if channel.registry {
		statements = clearRegistryChannelStatements
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, channel.id); err != nil {
			return fmt.Errorf("rebuild: clear derived state of channel %d: %w", channel.id, err)
		}
	}
	if !channel.registry {
		return nil
	}
	for _, statement := range clearEveryBucketStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild: clear bucket state for registry channel %d: %w", channel.id, err)
		}
	}
	return nil
}
