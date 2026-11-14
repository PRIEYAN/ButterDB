package channelrepo

import (
	"context"
	"database/sql"
	"fmt"
)

const SpoolBatchSize = 256

type SpoolMessage struct {
	MsgID        int64
	Date         int64
	FromID       int64
	Text         string
	HasMedia     bool
	MediaSize    int64
	DocumentName string
}

func (r *Repository) ClearSpool(ctx context.Context, channelID int64) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM initial_scan_spool WHERE channel_id = ?1`, channelID)
		return err
	})
	if err != nil {
		return fmt.Errorf("channelrepo: clear spool %d: %w", channelID, err)
	}
	return nil
}

func (r *Repository) SpoolPage(ctx context.Context, channelID int64, msgs []SpoolMessage) error {
	for start := 0; start < len(msgs); start += SpoolBatchSize {
		end := min(start+SpoolBatchSize, len(msgs))
		batch := msgs[start:end]
		err := r.db.Write(ctx, func(tx *sql.Tx) error {
			return insertSpoolBatch(ctx, tx, channelID, batch)
		})
		if err != nil {
			return fmt.Errorf("channelrepo: spool page %d: %w", channelID, err)
		}
	}
	return nil
}

func insertSpoolBatch(ctx context.Context, tx Tx, channelID int64, batch []SpoolMessage) error {
	for _, msg := range batch {
		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO initial_scan_spool (channel_id, msg_id, from_id, date, text, has_media, media_size, document_name)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
			channelID, msg.MsgID, msg.FromID, msg.Date, msg.Text, boolInt(msg.HasMedia), msg.MediaSize, msg.DocumentName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) SpooledPageTx(ctx context.Context, tx Tx, channelID, afterMsgID int64, limit int) ([]SpoolMessage, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("channelrepo: read spool %d: %w: limit %d", channelID, ErrInvalidChannel, limit)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT msg_id, from_id, date, text, has_media, media_size, document_name FROM initial_scan_spool
WHERE channel_id = ?1 AND msg_id > ?2 ORDER BY msg_id LIMIT ?3`, channelID, afterMsgID, limit)
	if err != nil {
		return nil, fmt.Errorf("channelrepo: read spool %d: %w", channelID, err)
	}
	defer rows.Close()
	page := []SpoolMessage{}
	for rows.Next() {
		var msg SpoolMessage
		var hasMedia int
		if err := rows.Scan(&msg.MsgID, &msg.FromID, &msg.Date, &msg.Text, &hasMedia, &msg.MediaSize, &msg.DocumentName); err != nil {
			return nil, fmt.Errorf("channelrepo: read spool %d: %w", channelID, err)
		}
		msg.HasMedia = hasMedia != 0
		page = append(page, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channelrepo: read spool %d: %w", channelID, err)
	}
	return page, nil
}
