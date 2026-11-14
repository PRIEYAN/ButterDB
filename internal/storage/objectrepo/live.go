package objectrepo

import (
	"context"
	"database/sql"
	"fmt"
)

type MessageRefs struct {
	Key           string
	PutMsgID      int64
	BackingMsgIDs []int64
}

const selectLive = `SELECT o.object_key, o.put_msg_id, o.body_msg_id
FROM objects o
WHERE o.channel_id = ?1
  AND NOT EXISTS (
    SELECT 1 FROM object_tombstones t
    WHERE t.channel_id = o.channel_id AND t.object_key = o.object_key AND t.del_msg_id > o.put_msg_id)
ORDER BY o.put_msg_id`

func (r *Repository) LiveMessageIDs(ctx context.Context, channelID int64) ([]MessageRefs, error) {
	refs := []MessageRefs{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, selectLive, channelID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref MessageRefs
			var bodyMsgID int64
			if err := rows.Scan(&ref.Key, &ref.PutMsgID, &bodyMsgID); err != nil {
				return err
			}
			ref.BackingMsgIDs = []int64{ref.PutMsgID}
			if bodyMsgID > 0 {
				ref.BackingMsgIDs = append(ref.BackingMsgIDs, bodyMsgID)
			}
			refs = append(refs, ref)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("objectrepo: list live message ids: %w", err)
	}
	return refs, nil
}
