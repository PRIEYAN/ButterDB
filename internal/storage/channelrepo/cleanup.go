package channelrepo

import (
	"context"
	"database/sql"
	"fmt"
)

var cleanupTables = []string{
	"replay_log",
	"replay_log_rejects",
	"replay_log_tamper",
	"projection_operations",
	"deleted_messages",
	"initial_scan_spool",
}

var channelScopedTables = []string{
	"replay_log",
	"replay_log_rejects",
	"replay_log_tamper",
	"registry_markers",
	"objects",
	"object_tombstones",
	"object_bodies",
	"projection_operations",
	"initial_scan_spool",
	"deleted_messages",
	"pending_message_cleanup",
	"pending_channel_cleanup",
	"buckets",
	"channels",
}

func (r *Repository) CompleteChannelCleanup(ctx context.Context, channelID int64) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return r.CompleteChannelCleanupTx(ctx, tx, channelID)
	})
	if err != nil {
		return fmt.Errorf("channelrepo: complete channel cleanup %d: %w", channelID, err)
	}
	return nil
}

func (r *Repository) CompleteChannelCleanupTx(ctx context.Context, tx Tx, channelID int64) error {
	if channelID <= 0 {
		return fmt.Errorf("channelrepo: complete channel cleanup: %w: id %d", ErrInvalidChannel, channelID)
	}
	if err := deleteScoped(ctx, tx, `pending_channel_cleanup`, channelID); err != nil {
		return err
	}
	for _, table := range cleanupTables {
		if err := deleteScoped(ctx, tx, table, channelID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) DeleteChannel(ctx context.Context, channelID int64) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return r.DeleteChannelTx(ctx, tx, channelID)
	})
	if err != nil {
		return fmt.Errorf("channelrepo: delete channel %d: %w", channelID, err)
	}
	return nil
}

func (r *Repository) DeleteChannelTx(ctx context.Context, tx Tx, channelID int64) error {
	if channelID <= 0 {
		return fmt.Errorf("channelrepo: delete channel: %w: id %d", ErrInvalidChannel, channelID)
	}
	for _, table := range channelScopedTables {
		if err := deleteScoped(ctx, tx, table, channelID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encryption_keys WHERE bucket_channel_id = ?1`, channelID); err != nil {
		return fmt.Errorf("channelrepo: delete encryption_keys %d: %w", channelID, err)
	}
	return nil
}

func deleteScoped(ctx context.Context, tx Tx, table string, channelID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE channel_id = ?1`, channelID); err != nil {
		return fmt.Errorf("channelrepo: delete %s %d: %w", table, channelID, err)
	}
	return nil
}
