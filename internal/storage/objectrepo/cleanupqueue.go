package objectrepo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

const (
	cleanupBackoffBaseMillis = 60 * 1000
	cleanupBackoffCapMillis  = 60 * 60 * 1000
	cleanupBackoffMaxShift   = 20
)

type CleanupDatabase interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type CleanupQueue struct {
	db CleanupDatabase
}

func NewCleanupQueue(db CleanupDatabase) (*CleanupQueue, error) {
	if db == nil {
		return nil, fmt.Errorf("objectrepo: %w: cleanup database", errs.ErrMissingDependency)
	}
	return &CleanupQueue{db: db}, nil
}

const selectDueChannels = `SELECT DISTINCT channel_id FROM pending_message_cleanup
WHERE next_attempt_at <= ?1 ORDER BY channel_id LIMIT ?2`

func (q *CleanupQueue) DueCleanupChannels(ctx context.Context, now int64, limit int) ([]int64, error) {
	var channels []int64
	err := q.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectDueChannels, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var channelID int64
			if err := rows.Scan(&channelID); err != nil {
				return err
			}
			channels = append(channels, channelID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("objectrepo: due cleanup channels: %w", err)
	}
	return channels, nil
}

const dropLiveCleanupRows = `DELETE FROM pending_message_cleanup
WHERE channel_id = ?1 AND msg_id IN (
  SELECT put_msg_id FROM objects WHERE channel_id = ?1
  UNION
  SELECT body_msg_id FROM objects WHERE channel_id = ?1 AND body_msg_id > 0)`

const selectDueBatch = `SELECT msg_id FROM pending_message_cleanup
WHERE channel_id = ?1 AND next_attempt_at <= ?2 ORDER BY next_attempt_at, msg_id LIMIT ?3`

func (q *CleanupQueue) ClaimCleanupBatch(ctx context.Context, channelID, now int64, limit int) ([]int64, error) {
	limit = min(limit, limits.DeleteBatchSize)
	if limit <= 0 {
		return nil, nil
	}
	var batch []int64
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		batch = nil
		if _, err := tx.ExecContext(ctx, dropLiveCleanupRows, channelID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, selectDueBatch, channelID, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var msgID int64
			if err := rows.Scan(&msgID); err != nil {
				return err
			}
			batch = append(batch, msgID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("objectrepo: claim cleanup batch for channel %d: %w", channelID, err)
	}
	return batch, nil
}

func (q *CleanupQueue) ConfirmCleanupDeleted(ctx context.Context, channelID int64, msgIDs []int64, recordedAt int64) error {
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		for _, msgID := range msgIDs {
			if _, err := tx.ExecContext(ctx, "DELETE FROM pending_message_cleanup WHERE channel_id = ?1 AND msg_id = ?2", channelID, msgID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, ?2, 'cleanup', ?3)", channelID, msgID, recordedAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("objectrepo: confirm cleanup for channel %d: %w", channelID, err)
	}
	return nil
}

const deferCleanupRow = `UPDATE pending_message_cleanup SET
  next_attempt_at = ?3 + MIN(?5 * (1 << MIN(attempts, ?7)), ?6),
  attempts = attempts + 1,
  last_error = ?4
WHERE channel_id = ?1 AND msg_id = ?2`

func (q *CleanupQueue) DeferCleanup(ctx context.Context, channelID int64, msgIDs []int64, nextAttemptAt int64, lastError string) error {
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		for _, msgID := range msgIDs {
			_, err := tx.ExecContext(ctx, deferCleanupRow, channelID, msgID, nextAttemptAt, lastError,
				cleanupBackoffBaseMillis, cleanupBackoffCapMillis, cleanupBackoffMaxShift)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("objectrepo: defer cleanup for channel %d: %w", channelID, err)
	}
	return nil
}

func (q *CleanupQueue) BodyChannels(ctx context.Context) ([]int64, error) {
	var channels []int64
	err := q.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT channel_id FROM object_bodies ORDER BY channel_id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var channelID int64
			if err := rows.Scan(&channelID); err != nil {
				return err
			}
			channels = append(channels, channelID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("objectrepo: body channels: %w", err)
	}
	return channels, nil
}

const selectUnreferencedBodies = `SELECT b.msg_id, b.job_id FROM object_bodies b
WHERE b.channel_id = ?1 AND b.msg_date <= ?2 AND b.msg_id > ?3
  AND NOT EXISTS (SELECT 1 FROM objects o WHERE o.channel_id = b.channel_id AND o.body_msg_id = b.msg_id)
  AND NOT EXISTS (SELECT 1 FROM pending_message_cleanup p WHERE p.channel_id = b.channel_id AND p.msg_id = b.msg_id)
  AND NOT EXISTS (SELECT 1 FROM deleted_messages d WHERE d.channel_id = b.channel_id AND d.msg_id = b.msg_id)
ORDER BY b.msg_id LIMIT ?4`

func (q *CleanupQueue) OrphanBodies(ctx context.Context, channelID, olderThanUnix, afterMsgID int64, limit int) ([]int64, []string, error) {
	var msgIDs []int64
	var jobIDs []string
	err := q.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectUnreferencedBodies, channelID, olderThanUnix, afterMsgID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var msgID int64
			var jobID string
			if err := rows.Scan(&msgID, &jobID); err != nil {
				return err
			}
			msgIDs = append(msgIDs, msgID)
			jobIDs = append(jobIDs, jobID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("objectrepo: orphan bodies of channel %d: %w", channelID, err)
	}
	return msgIDs, jobIDs, nil
}

const queueOrphanBody = `INSERT INTO pending_message_cleanup (channel_id, msg_id, reason, queued_by_msg_id)
SELECT ?1, ?2, 'orphan_body', 0
WHERE NOT EXISTS (SELECT 1 FROM objects WHERE channel_id = ?1 AND (put_msg_id = ?2 OR body_msg_id = ?2))
  AND NOT EXISTS (SELECT 1 FROM deleted_messages WHERE channel_id = ?1 AND msg_id = ?2)
ON CONFLICT (channel_id, msg_id) DO NOTHING`

func (q *CleanupQueue) QueueOrphanBodies(ctx context.Context, channelID int64, msgIDs []int64) (int, error) {
	queued := 0
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		queued = 0
		for _, msgID := range msgIDs {
			result, err := tx.ExecContext(ctx, queueOrphanBody, channelID, msgID)
			if err != nil {
				return err
			}
			inserted, err := result.RowsAffected()
			if err != nil {
				return err
			}
			queued += int(inserted)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("objectrepo: queue orphan bodies of channel %d: %w", channelID, err)
	}
	return queued, nil
}
