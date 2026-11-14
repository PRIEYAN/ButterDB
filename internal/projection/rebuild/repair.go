package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
)

func (r *Rebuilder) NeedsKeyRepair(ctx context.Context, tx projection.Tx, channelID int64, key string, msgID int64) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM replay_log
		WHERE channel_id = ?1 AND object_key = ?2 AND msg_id > ?3
		LIMIT 1
	`, channelID, key, msgID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("rebuild: check key order in channel %d: %w", channelID, err)
	}
	return true, nil
}

func (r *Rebuilder) RepairKey(ctx context.Context, w projection.Writer, channelID int64, key string) (Result, error) {
	var total Result
	err := r.inWrite(ctx, w, func(tx *sql.Tx) error {
		var err error
		total, err = r.RepairKeyTx(ctx, tx, channelID, key)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return total, nil
}

func (r *Rebuilder) RepairKeyTx(ctx context.Context, tx projection.Tx, channelID int64, key string) (Result, error) {
	channel, err := loadChannel(ctx, tx, channelID)
	if err != nil {
		return Result{}, err
	}
	if channel.registry || channel.deleted {
		return Result{}, fmt.Errorf("rebuild: channel %d: %w", channelID, errs.ErrNoSuchBucket)
	}
	if err := clearKeyState(ctx, tx, channelID, key); err != nil {
		return Result{}, err
	}
	result, err := r.replay(ctx, tx, channelID, replayScope{clause: "AND object_key = ?", args: []any{key}})
	if err != nil {
		return Result{}, err
	}
	r.logger.Info("rebuild: key repaired", "channel_id", channelID, "applied", result.Applied, "rejected", result.Rejected)
	return result, nil
}

func clearKeyState(ctx context.Context, tx projection.Tx, channelID int64, key string) error {
	if err := backOutObject(ctx, tx, channelID, key); err != nil {
		return err
	}
	statements := []string{
		`DELETE FROM object_tombstones WHERE channel_id = ?1 AND object_key = ?2`,
		`DELETE FROM projection_operations WHERE channel_id = ?1 AND msg_id IN (SELECT msg_id FROM replay_log WHERE channel_id = ?1 AND object_key = ?2)`,
		`DELETE FROM replay_log_rejects WHERE channel_id = ?1 AND msg_id IN (SELECT msg_id FROM replay_log WHERE channel_id = ?1 AND object_key = ?2)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, channelID, key); err != nil {
			return fmt.Errorf("rebuild: clear key state in channel %d: %w", channelID, err)
		}
	}
	return nil
}

func backOutObject(ctx context.Context, tx projection.Tx, channelID int64, key string) error {
	var storedSize, plaintextSize int64
	err := tx.QueryRowContext(ctx, `SELECT stored_size, plaintext_size FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key).
		Scan(&storedSize, &plaintextSize)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rebuild: read object in channel %d: %w", channelID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE channel_id = ? AND object_key = ?`, channelID, key); err != nil {
		return fmt.Errorf("rebuild: remove object in channel %d: %w", channelID, err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE buckets
		SET object_count = object_count - 1, stored_bytes = stored_bytes - ?2, plaintext_bytes = plaintext_bytes - ?3
		WHERE channel_id = ?1
	`, channelID, storedSize, plaintextSize)
	if err != nil {
		return fmt.Errorf("rebuild: back out bucket counters in channel %d: %w", channelID, err)
	}
	return nil
}
