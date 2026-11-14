package apply

import (
	"context"
	"database/sql"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

const (
	cleanupSuperseded = "superseded"
	cleanupBlocked    = "blocked"
	cleanupDuplicate  = "duplicate"
	cleanupDeleted    = "deleted"
)

const queueCleanupSQL = `INSERT OR IGNORE INTO pending_message_cleanup (channel_id, msg_id, reason, object_key, queued_by_msg_id)
SELECT ?1, ?2, ?3, ?4, ?5
WHERE NOT EXISTS (SELECT 1 FROM deleted_messages WHERE channel_id = ?1 AND msg_id = ?2)
  AND NOT EXISTS (SELECT 1 FROM objects WHERE channel_id = ?1 AND (put_msg_id = ?2 OR body_msg_id = ?2))`

func queueCleanup(ctx context.Context, tx projection.Tx, channelID int64, key string, queuedBy int64, reason string, msgIDs ...int64) error {
	for _, msgID := range msgIDs {
		if msgID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, queueCleanupSQL, channelID, msgID, reason, key, queuedBy); err != nil {
			return sqlFailure("queue message cleanup", err)
		}
	}
	return nil
}

func isMessageDeleted(ctx context.Context, tx projection.Tx, channelID, msgID int64) (bool, error) {
	if msgID <= 0 {
		return false, nil
	}
	var present int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM deleted_messages WHERE channel_id = ?1 AND msg_id = ?2`, channelID, msgID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sqlFailure("load deleted message", err)
	}
	return true, nil
}
